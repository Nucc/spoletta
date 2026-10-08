// Gives the agent the MCP servers its process uses, and no others. The worker
// loads it for a run that uses any, listing them in the JSON file
// FLOW_MCP_SERVERS names, with the token for the webapp's proxy, which every
// server's URL points to.

import { readFileSync } from "node:fs";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Spec = {
	servers: { name: string; url: string; description?: string }[];
	token: string;
};

export default function (pi: ExtensionAPI) {
	const specFile = process.env.FLOW_MCP_SERVERS;
	if (!specFile) return;
	const spec: Spec = JSON.parse(readFileSync(specFile, "utf8"));

	// Registered while loading, so they connect when the session starts.
	for (const s of spec.servers) {
		pi.registerMcpServer(s.name, {
			url: s.url,
			headers: { Authorization: `Bearer ${spec.token}` },
			...(s.description ? { description: s.description } : {}),
		});
	}
}
