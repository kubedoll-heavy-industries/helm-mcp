import { Container, ContainerProxy, getContainer } from "@cloudflare/containers";

export { ContainerProxy };

interface Env {
  MCP_HELM: DurableObjectNamespace<McpHelmContainer>;
}

export class McpHelmContainer extends Container {
  defaultPort = 8012;
  enableInternet = true;
  sleepAfter = "5m";
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const container = await getContainer(env.MCP_HELM, "mcp-helm");
    return container.fetch(request);
  },
};
