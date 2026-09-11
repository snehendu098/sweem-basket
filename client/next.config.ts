import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  reactCompiler: true,
  // Emits a self-contained server bundle with only the modules actually
  // imported, so the runtime image drops from ~2GB to ~200MB.
  output: "standalone",
};

export default nextConfig;
