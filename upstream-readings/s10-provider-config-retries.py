# Source: aider/models.py  (lines 985-1082, annotated + lightly trimmed)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/models.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). This is a verbatim
# excerpt reproduced under Apache-2.0 for study; only comments were added and a
# couple of edge branches (github-copilot token swap, ollama num_ctx) trimmed for
# readability. The original file is 1338 lines.
#
# WHY THIS EXCERPT FOR s10
# ------------------------
# These two methods on aider's `Model` are the load-bearing pair s10 reimplements:
#   send_completion          -> build kwargs + ONE call to litellm.completion  (L985)
#   simple_send_with_retries -> the retry loop with doubling backoff           (L1039)
# The retry loop is the direct ancestor of s10's withRetry / RetryProvider:
# `retry_delay = 0.125`, `retry_delay *= 2`, give up once it passes RETRY_TIMEOUT.
# The verdict "is this error transient?" comes from litellm's exception taxonomy
# (`ex_info.retry`); s10 reads the HTTP status code instead (429/5xx -> retry).


def send_completion(self, messages, functions, stream, temperature=None):
    # `self.name` is the canonical model id from the registry — the same thing
    # s10's ModelConfig.Name carries. litellm routes it to the right backend.
    kwargs = dict(
        model=self.name,
        stream=stream,                       # s10: CreateMessageRequest.Stream
    )

    # use_temperature is one of the per-model ModelSettings knobs. s10 keeps the
    # analogous defaults in ModelConfig.ExtraParams (e.g. deepseek temperature=0).
    if self.use_temperature is not False:
        if temperature is None:
            if isinstance(self.use_temperature, bool):
                temperature = 0
            else:
                temperature = float(self.use_temperature)
        kwargs["temperature"] = temperature

    # Tool/function calling: aider sends exactly ONE function and forces it.
    if functions is not None:
        function = functions[0]
        kwargs["tools"] = [dict(type="function", function=function)]
        kwargs["tool_choice"] = {"type": "function", "function": {"name": function["name"]}}

    # extra_params is the per-model "bag" merged into every request. This is
    # exactly s10's ModelConfig.ExtraParams.
    if self.extra_params:
        kwargs.update(self.extra_params)

    if "timeout" not in kwargs:
        kwargs["timeout"] = request_timeout
    kwargs["messages"] = messages

    # THE actual LLM call. litellm hides ~50 provider SDKs behind this one line —
    # that whole abstraction is what s10's Provider interface stands in for.
    res = litellm.completion(**kwargs)
    return hash_object, res


def simple_send_with_retries(self, messages):
    from aider.exceptions import LiteLLMExceptions

    litellm_ex = LiteLLMExceptions()
    if "deepseek-reasoner" in self.name:
        messages = ensure_alternating_roles(messages)

    retry_delay = 0.125                      # s10: RetryConfig.BaseDelay (125ms)

    while True:
        try:
            kwargs = {
                "messages": messages,
                "functions": None,
                "stream": False,
            }
            _hash, response = self.send_completion(**kwargs)
            if not response or not hasattr(response, "choices") or not response.choices:
                return None
            res = response.choices[0].message.content
            from aider.reasoning_tags import remove_reasoning_content
            return remove_reasoning_content(res, self.reasoning_tag)

        except litellm_ex.exceptions_tuple() as err:
            ex_info = litellm_ex.get_ex_info(err)
            print(str(err))
            if ex_info.description:
                print(ex_info.description)

            # THE retry gate. ex_info.retry is litellm's verdict on whether the
            # error is transient. s10's isRetryable() makes the same call from the
            # HTTP status code: 429 or 5xx -> retry, everything else -> fail fast.
            should_retry = ex_info.retry
            if should_retry:
                retry_delay *= 2             # s10: delay *= 2, capped at MaxDelay
                if retry_delay > RETRY_TIMEOUT:
                    should_retry = False     # give up once past the cap
            if not should_retry:
                return None

            print(f"Retrying in {retry_delay:.1f} seconds...")
            time.sleep(retry_delay)
            continue
        except AttributeError:
            return None


# READING MAP
# -----------
# 1. ModelSettings (L127) + MODEL_ALIASES (L99): the dataclass of per-model knobs
#    (edit_format, use_temperature, extra_params, streaming) and the alias table.
#    -> s10's ModelConfig + the `aliases`/`registry` maps + LookupModel.
# 2. send_completion (above, L985): build kwargs, merge extra_params, ONE
#    litellm.completion call.  -> s10's Provider.CreateMessage + ModelConfig.
# 3. simple_send_with_retries (above, L1039): the while-True backoff loop.
#    -> s10's withRetry / RetryProvider (retry_delay *= 2, cap, transient gate).
# 4. streaming: ModelSettings.streaming (L144) flips stream=True; the streamed
#    chunks are reassembled by the Coder upstream. s10 decodes Anthropic SSE
#    directly in accumulateStream (provider.go) instead of litellm's chunks.
# 5. aider/llm.py LazyLiteLLM (L21-47): the deferred `import litellm`. s10 has no
#    equivalent — Go has no 1.5s import cost — so we omit the lazy wrapper.
