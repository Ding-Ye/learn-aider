---
title: "多模型接入指南"
slug: multi-model
est_read_min: 11
---

# 多模型接入指南

> 本课程把"调哪个大模型"抽象成了一个 `Provider` 接口。于是同一套章节代码，既能跑在 Anthropic Claude 上，也能一行切换到 DeepSeek、Qwen（通义千问）、Moonshot/Kimi、Groq、OpenRouter，甚至你自己用 vLLM/SGLang 托管的开源模型上——不用改一行业务代码。

## 为什么要多模型

从 s01 到 s10，每一章演示的都是 coder agent 的某个机制：whole-file 编辑、SEARCH/REPLACE 块、unified diff、prompt 组织、repo map、linter 反思……这些机制**和具体哪家模型无关**。如果课程把自己钉死在某一家 API 上，价值就少了一半：

- 国内同学手里多半是 **DeepSeek / Qwen / Kimi** 的 key，没必要为了跑个 demo 去申请别的。
- 想省钱时，可以用便宜的小模型（甚至本地模型）跑那些不吃智力的步骤，把贵模型留给真正难的编辑。
- 想离线、想自托管时，**vLLM / SGLang** 暴露的就是 OpenAI 兼容端点，应当开箱即用。

所以本课程刻意把 LLM 调用收敛到一个接口后面。换模型 = 换一个实现 + 换一个 base URL + 换一个 API key 环境变量，仅此而已。这正是上游 aider 用 litellm 想达到的效果，我们用最小的 Go 代码把承重部分复刻了出来。

## Provider 抽象

整个秘诀是：**程序内部只认一种数据形状**——Anthropic 的 Messages 格式（`Message` / `ContentBlock`，块类型是 `text` / `tool_use` / `tool_result`，停止原因是 `stop_reason`）。这个形状是一个干净的 tagged union，能平滑映射到其他所有家的协议。接口本身极小，只有一个方法（见 `agents/s01-minimum-coder-loop/provider.go`）：

```go
type Provider interface {
    CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}
```

它有两个实现，对外形状完全一致：

- **`AnthropicProvider`**（`agents/s01-minimum-coder-loop/provider.go`）——直连 `https://api.anthropic.com/v1/messages`。因为内部形状*就是* Anthropic 的 wire format，这里**没有任何翻译**。
- **`OpenAIProvider`**（`agents/s01-minimum-coder-loop/provider_openai.go`）——对接*任意* OpenAI 兼容的 Chat Completions API。它在 provider 边界做**双向翻译**：

  ```
  Anthropic 形状的请求  ->  OpenAI Chat Completions 请求
  OpenAI Chat Completions 响应  ->  Anthropic 形状的响应
  ```

  请求方向（`translateRequestToOpenAI`）做三件事：system prompt 变成 `{role:"system"}` 消息；每条 Anthropic 消息展开成 1 条或多条 OpenAI 消息（assistant 的 `tool_use` 块变成 `tool_calls`，user 的 `tool_result` 块各自变成一条 `{role:"tool"}` 消息）；工具定义包进 `{type:"function", function:{...}}`。响应方向（`translateResponseFromOpenAI`）把 `finish_reason` 映射回 `stop_reason`（`stop`→`end_turn`、`tool_calls`→`tool_use`、`length`→`max_tokens`），并兼容某些 provider 把 `content` 返回成数组而非字符串的情况（`contentToString`）。

把翻译只做在边界这一处，意味着 `coder.go`、`main.go` 以及后面所有章节，无论用户选了哪家模型，代码都**一字不改**。

到了 **s10**，这个抽象被推到完整形态（`agents/s10-provider-config-retries/`）：

- **模型注册表**（`agents/s10-provider-config-retries/models.go`）——一张 `map[string]ModelConfig`，把模型名映射到 `{后端, baseURL, API key 环境变量, 上下文窗口, 默认编辑格式}`。还有一张 `aliases` 表，让你能用 `sonnet`、`deepseek`、`kimi`、`qwen` 这类短名。`buildProvider` 是**唯一**知道具体 provider 类型的地方——加一个后端就是加一行数据，不是写分支。
- **重试装饰器**（`agents/s10-provider-config-retries/retry.go`）——`RetryProvider` 既*是* `Provider` 又*包* `Provider`，用指数退避 + 抖动扛 429/5xx。因为两个 provider 的错误都用同一个 `statusError` 类型，重试逻辑不关心背后是哪家后端，分类瞬时/致命的方式完全一致。

## 一行切换

s01 是用户最先碰到的入口，它在 `main.go` 里把整套 8 个 provider profile 直接暴露成命令行 flag：`-provider` 选档位、`-base-url` 覆盖端点、`-model` 覆盖模型 id。用法就是把 provider 名字换一下：

```bash
cd agents/s01-minimum-coder-loop

# 默认：Anthropic Claude
export ANTHROPIC_API_KEY=sk-ant-...
go run . hello.go "给 main 加一句文档注释"

# 切到 DeepSeek，业务代码零改动
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -model deepseek-chat hello.go "把 foo 重命名为 bar"

# 切到 Qwen（通义千问，走 DashScope 的 OpenAI 兼容端点）
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen hello.go "修掉这个拼写错误"

# 本地自托管（vLLM / SGLang）——用 -base-url 指向你的端点
go run . -provider local -base-url http://localhost:8000/v1 -model your-model app.py "..."
```

每个 profile 对应一个端点、一个默认模型、一个读取 key 的环境变量：

| `-provider` | endpoint | 默认 model | API key 环境变量 |
|---|---|---|---|
| `anthropic`（默认） | api.anthropic.com（原生） | claude-sonnet-4-6 | `ANTHROPIC_API_KEY` |
| `openai` | api.openai.com/v1 | gpt-4o-mini | `OPENAI_API_KEY` |
| `deepseek` | api.deepseek.com/v1 | deepseek-chat | `DEEPSEEK_API_KEY` |
| `moonshot` | api.moonshot.cn/v1 | moonshot-v1-8k | `MOONSHOT_API_KEY` |
| `qwen` | dashscope.aliyuncs.com/compatible-mode/v1 | qwen-plus | `DASHSCOPE_API_KEY` |
| `groq` | api.groq.com/openai/v1 | llama-3.3-70b-versatile | `GROQ_API_KEY` |
| `openrouter` | openrouter.ai/api/v1 | openai/gpt-4o-mini | `OPENROUTER_API_KEY` |
| `local` | http://localhost:8000/v1 | local-model | `OPENAI_API_KEY` |

s01 的 `main.go` 里逻辑很直白：选 `anthropic` 就走 `NewAnthropicProvider`，其余一律走 `NewOpenAIProvider`（带上 profile 给的 base URL）。s10 则更进一步，用 `-model` 一个名字（比如 `-model sonnet`、`-model deepseek`、`-model r1`）经注册表解析出整套配置，再自动套上重试——`go run . -model deepseek "用一句话解释 goroutine"`。

其他章节（s02–s09）默认保持 Anthropic 即可，要给它们接别的模型也很简单：复制 s01 的 `providerProfiles` 表 + flag 解析，或者直接在建 provider 那一行改成 `NewOpenAIProvider(os.Getenv("DEEPSEEK_API_KEY"), "https://api.deepseek.com/v1", "deepseek-chat")`。

## 与上游 aider 的对应

aider 用 **litellm** 来做同一件事——一个统一接口屏蔽掉所有 provider。对照上游（pinned 在 `.learn/upstream`，sha `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`）：

- `aider/llm.py` 是个极薄的 `LazyLiteLLM` 包装，懒加载 litellm（因为 `import litellm` 要花 1.5 秒）。aider 所有的模型调用最终都落到 `litellm.completion(...)`。
- `aider/models.py` 里，`MODEL_ALIASES`（L99）给模型起短名，`class ModelSettings`（L128）描述每个模型的设置，`class Model`（L329）在 L334 把别名解析成真名，`send_completion`（L985）调 `litellm.completion`（L1036），`simple_send_with_retries`（L1039）用一个 `while True` 循环做退避重试。

我们 s10 的 `models.go` 就是 `ModelSettings` + `MODEL_ALIASES` 的极简版（注释里逐行标了上游对应位置），`retry.go` 就是 `simple_send_with_retries` 的 Go 移植。**区别在覆盖面**：litellm 一家就支持 50+ 个 provider，连各家千奇百怪的鉴权、参数、分块格式都内部归一化了；我们的 mini 只覆盖**「OpenAI 兼容的那一大类」+ Anthropic 原生**这两条路。好在主流模型几乎都提供 OpenAI 兼容端点，所以这两条路已经能覆盖绝大多数日常场景，而且代码全摊在你眼前——这正是课程的取舍：用可读性换覆盖面。

## 注意事项

抽象不是免费的午餐。换模型时留意这几点：

- **工具调用 / JSON 的差异。** 各家对 tool calling 的支持参差不齐：OpenRouter 上部分模型根本不支持 tools；本地 vLLM 要带 `--enable-auto-tool-choice` 才会真的发起 tool call。还有些 provider（DeepSeek 偶发）会把 `content` 返回成数组而不是字符串——翻译层的 `contentToString` 已经兜住了这种情况。正因如此，本课程的编辑章节大多**引导模型用围栏代码块（fenced block）回复，而不是依赖 tool call**，这样跨 provider 最稳。
- **上下文窗口差异很大。** 注册表里各模型的 `ContextWindow` 一目了然：Claude 是 200K、gpt-4o 128K、DeepSeek 64K，而 `moonshot-v1-8k` 只有 8K。喂大文件 / 大 repo map 给小窗口模型会直接超限，按模型量力而行。
- **默认参数因模型而异。** 比如注册表给 DeepSeek 预置了 `temperature: 0.0`（对齐 aider 让编辑更确定的做法），给小模型默认 whole-file 格式（比 diff 更可靠）。`max_tokens` 各家默认也不同，必要时显式设。
- **有些章节根本不调 LLM。** 课程里若干章是**纯机制演示**——比如 repo map 的 PageRank 排序、SEARCH/REPLACE 块的解析、unified diff 的应用，这些是确定性算法，不需要任何模型。对它们而言"多模型"无从谈起，选什么 provider 都不影响输出。
- **限流与免费额度。** Groq 免费档有 RPM 上限，跑批量任务容易撞墙——这正是 s10 那个重试装饰器存在的意义：429 会被自动退避重试，而不是直接报错退出。
