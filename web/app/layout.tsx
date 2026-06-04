import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "learn-aider",
  description:
    "Learn how aider really works by building a Go mini-implementation, chapter by chapter.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh">
      <body>{children}</body>
    </html>
  );
}
