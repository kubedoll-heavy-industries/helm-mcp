import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { GenericContainer, Wait } from "testcontainers";
import type { StartedTestContainer } from "testcontainers";
import type { GlobalSetupContext } from "vitest/node";

const execFileAsync = promisify(execFile);
const imageName = "mcp-helm-e2e:latest";

declare module "vitest" {
  export interface ProvidedContext {
    baseUrl: string;
  }
}

let container: StartedTestContainer;

export async function setup({ provide }: GlobalSetupContext) {
  const context = path.resolve(import.meta.dirname, "..");
  await execFileAsync("docker", ["build", "-t", imageName, context], {
    maxBuffer: 10 * 1024 * 1024,
  });

  container = await new GenericContainer(imageName)
    .withExposedPorts(8012)
    .withWaitStrategy(
      Wait.forHttp("/healthz", 8012).forStatusCode(200),
    )
    .withStartupTimeout(120_000)
    .start();

  // Use 127.0.0.1 explicitly to avoid IPv6 dual-stack issues with Node.js fetch
  const host = container.getHost().replace("localhost", "127.0.0.1");
  const baseUrl = `http://${host}:${container.getMappedPort(8012)}`;
  provide("baseUrl", baseUrl);
}

export async function teardown() {
  await container?.stop();
}
