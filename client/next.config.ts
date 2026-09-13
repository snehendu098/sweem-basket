import type { NextConfig } from "next";

// No `output: "standalone"`: that is for self-hosting in a container, and it
// omits the file-tracing manifests Vercel's builder expects. The client is
// deployed to Vercel; docker-compose does not build it.
const nextConfig: NextConfig = {
  reactCompiler: true,
};

export default nextConfig;
