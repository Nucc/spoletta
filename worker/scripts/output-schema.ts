// Checks the two ways an agent is given the submit_output tool against each
// other and the MCP server against what it is to answer. pi's extension
// (submit-output.ts) builds the tool's parameters with typebox, the MCP
// server Claude Code gets (submit-output-mcp.ts) builds them as plain JSON
// Schema (jsonSchema in output-spec.ts); for each spec below both are built
// and must be the same JSON, typebox's own marks aside. Then the server is
// started over a pipe, as Claude Code starts it, and driven through a session:
// what it answers, the problems a bad call comes back with, the output a good
// one writes, and that it ends with its stdin.
//
// typebox is only installed inside pi, so the schemas are compared when pi
// is installed (globally, or where PI_PACKAGE says), and skipped, saying so,
// when it isn't; the server needs nothing but Node. Run by
// output_schema_test.go when Node 22.6 or later is installed, which loads
// TypeScript as it is: node --experimental-strip-types scripts/output-schema.ts

import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import * as nodeModule from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath, pathToFileURL } from "node:url";
import { jsonSchema, toolDescription } from "../output-spec.ts";
import type { Attribute, Spec } from "../output-spec.ts";

const worker = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let failures = 0;
const fail = (what: string) => {
	failures++;
	console.error(`FAIL ${what}`);
};

// Where two JSON values first differ, or null when they are the same:
// objects key by key in any order, arrays item by item.
function difference(got: unknown, want: unknown, at = "$"): string | null {
	if (Array.isArray(got) || Array.isArray(want)) {
		if (!Array.isArray(got) || !Array.isArray(want)) return `${at}: ${JSON.stringify(got)} isn't ${JSON.stringify(want)}`;
		if (got.length !== want.length) return `${at}: ${got.length} items, want ${want.length}`;
		for (let i = 0; i < got.length; i++) {
			const d = difference(got[i], want[i], `${at}[${i}]`);
			if (d) return d;
		}
		return null;
	}
	if (typeof got === "object" && got !== null && typeof want === "object" && want !== null) {
		const g = got as Record<string, unknown>;
		const w = want as Record<string, unknown>;
		for (const k of Object.keys(w)) if (!Object.hasOwn(g, k)) return `${at}.${k} is missing`;
		for (const k of Object.keys(g)) if (!Object.hasOwn(w, k)) return `${at}.${k} isn't wanted`;
		for (const k of Object.keys(w)) {
			const d = difference(g[k], w[k], `${at}.${k}`);
			if (d) return d;
		}
		return null;
	}
	return Object.is(got, want) ? null : `${at}: ${JSON.stringify(got)} isn't ${JSON.stringify(want)}`;
}

function expect(what: string, got: unknown, want: unknown) {
	const d = difference(got, want);
	if (d) fail(`${what}: ${d}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`);
}

// --- The specs both schemas are built for.

const attr = (name: string, more: Partial<Attribute> = {}): Attribute => ({ name, description: "", file: false, ...more });
const nowhere = { filesDir: "/nowhere/files", resultFile: "/nowhere/result.json" };
const review = { name: "Pull request", states: ["approved", "changes requested"], review: true };

const specs: Record<string, Spec> = {
	"one object": {
		shape: "object",
		objects: [{ name: "Bug", many: false, attributes: [attr("title", { type: "text", description: "What is wrong" }), attr("body"), attr("severity", { type: "integer" })] }],
		...nowhere,
	},
	"a list": {
		shape: "items",
		objects: [{ name: "Ticket", many: true, minItems: 1, maxItems: 5, attributes: [attr("key", { type: "text", pattern: "^[A-Z]+-[0-9]+$" }), attr("summary")] }],
		...nowhere,
	},
	"a list without bounds": {
		shape: "items",
		objects: [{ name: "Ticket", many: true, attributes: [attr("summary", { description: "In a line" })] }],
		...nowhere,
	},
	"several objects": {
		shape: "objects",
		objects: [
			{ name: "Report", many: false, attributes: [attr("text", { type: "text" }), attr("file", { file: true, description: "The report" })] },
			{ name: "Findings", many: true, minItems: 2, attributes: [attr("title"), attr("line", { type: "integer" })] },
			{ name: "Labels", many: true, minItems: 0, maxItems: 0, attributes: [attr("name")] },
		],
		...nowhere,
	},
	"decisions only": {
		shape: "decisions",
		objects: [],
		decisions: [review, { name: "Bug", states: ["open"], review: false }],
		...nowhere,
	},
	"an object and decisions": {
		shape: "object",
		objects: [{ name: "Review", many: false, attributes: [attr("summary", { type: "text" })] }],
		decisions: [review],
		...nowhere,
	},
	"a list and decisions": {
		shape: "items",
		objects: [{ name: "Comment", many: true, maxItems: 10, attributes: [attr("body"), attr("line", { type: "integer" })] }],
		decisions: [review],
		...nowhere,
	},
	"objects and decisions": {
		shape: "objects",
		objects: [
			{ name: "Review", many: false, attributes: [attr("summary")] },
			{ name: "Comments", many: true, attributes: [attr("body")] },
		],
		decisions: [review, { name: "Ticket", states: ["done", "blocked"], review: false }],
		...nowhere,
	},
	"nested parts": {
		shape: "object",
		objects: [
			{
				name: "Release",
				many: false,
				attributes: [
					attr("notes", {
						description: "What it brings",
						attributes: [
							attr("headline", { type: "text" }),
							attr("changes", {
								many: true,
								minItems: 1,
								maxItems: 20,
								description: "Each change",
								attributes: [attr("title"), attr("author", { attributes: [attr("name"), attr("email", { type: "text", pattern: ".+@.+" })] })],
							}),
						],
					}),
					attr("version", { type: "text" }),
				],
			},
		],
		...nowhere,
	},
	"typed attributes": {
		shape: "object",
		objects: [
			{
				name: "Measurement",
				many: false,
				attributes: [
					attr("label", { type: "text", pattern: "^[a-z-]+$", description: "Its label" }),
					attr("count", { type: "integer", description: "How many" }),
					attr("ratio", { type: "number" }),
					attr("valid", { type: "boolean", description: "Whether it holds" }),
					attr("taken", { type: "datetime", description: "When" }),
					attr("report", { type: "markdown", description: "Text in Markdown. What was found" }),
					attr("raw", { description: "Anything" }),
					attr("unknown", { type: "colour" }),
				],
			},
		],
		...nowhere,
	},
	"file attributes": {
		shape: "objects",
		objects: [
			{ name: "Report", many: false, attributes: [attr("pdf", { file: true }), attr("image", { file: true, type: "text", description: "A picture" })] },
			{ name: "Attachments", many: true, attributes: [attr("file", { file: true, description: "Where it is" }), attr("parts", { many: true, attributes: [attr("file", { file: true })] })] },
		],
		...nowhere,
	},
	"many attributes without their own": {
		shape: "object",
		objects: [
			{
				name: "Tags",
				many: false,
				attributes: [
					attr("names", { many: true, description: "Each name", minItems: 1, maxItems: 3 }),
					attr("others", { many: true }),
					attr("counts", { many: true, type: "integer", maxItems: 0 }),
					attr("empty", { many: true, attributes: [] }),
				],
			},
		],
		...nowhere,
	},
	"a decision without states": {
		shape: "decisions",
		objects: [],
		decisions: [{ name: "Stuck", states: [], review: false }],
		...nowhere,
	},
};

// --- pi's schemas against the MCP server's.

// typebox as pi has it, beside or inside the pi package, or null.
function findTypebox(): { entry: string; looked: string[] } | { entry: null; looked: string[] } {
	const pis: string[] = [];
	if (process.env.PI_PACKAGE) pis.push(process.env.PI_PACKAGE);
	pis.push(join(dirname(process.execPath), "..", "lib", "node_modules", "@earendil-works", "pi-coding-agent"));
	try {
		const root = execFileSync("npm", ["root", "-g"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"], timeout: 20_000 }).trim();
		if (root) pis.push(join(root, "@earendil-works", "pi-coding-agent"));
	} catch {
		// No npm: the places above are all there is.
	}
	for (const pi of pis) {
		if (!existsSync(join(pi, "package.json"))) continue;
		try {
			return { entry: nodeModule.createRequire(join(pi, "package.json")).resolve("typebox"), looked: pis };
		} catch {
			// pi without typebox where it can be found; try the next.
		}
	}
	return { entry: null, looked: pis };
}

// Has `import ... from "typebox"` in submit-output.ts load the typebox found,
// which Node wouldn't find from the worker's directory on its own.
function resolveTypebox(entry: string) {
	const url = pathToFileURL(entry).href;
	const registerHooks = (nodeModule as { registerHooks?: (hooks: object) => void }).registerHooks;
	if (registerHooks) {
		registerHooks({
			resolve: (specifier: string, context: unknown, next: (s: string, c: unknown) => unknown) =>
				specifier === "typebox" ? { url, shortCircuit: true } : next(specifier, context),
		});
		return;
	}
	const hooks = `export async function resolve(s, c, next) { return s === "typebox" ? { url: ${JSON.stringify(url)}, shortCircuit: true } : next(s, c); }`;
	nodeModule.register(`data:text/javascript,${encodeURIComponent(hooks)}`);
}

async function compareSchemas() {
	const { entry, looked } = findTypebox();
	if (!entry) {
		console.log(`skipped comparing the schemas: typebox, which only pi has, isn't installed (looked for pi in ${looked.join(", ")})`);
		return;
	}
	resolveTypebox(entry);
	const { toolParameters } = (await import("../submit-output.ts")) as { toolParameters: (spec: Spec) => unknown };
	// As JSON, which leaves out typebox's marks (~kind, ~optional) and the
	// descriptions it holds as undefined.
	const asJSON = (v: unknown) => JSON.parse(JSON.stringify(v));
	for (const [name, spec] of Object.entries(specs)) {
		const pi = asJSON(toolParameters(spec));
		const d = difference(asJSON(jsonSchema(spec)), pi);
		if (d) fail(`${name}: the MCP server's schema differs from pi's at ${d}\n  pi's  ${JSON.stringify(pi)}\n  the server's ${JSON.stringify(jsonSchema(spec))}`);
	}
	// The comparison has to see a difference that matters.
	const changed = asJSON(toolParameters(specs["nested parts"]));
	changed.properties.notes.properties.changes.description = "Each one";
	if (!difference(asJSON(jsonSchema(specs["nested parts"])), changed)) fail("the schemas' comparison misses a changed description");
	console.log(`ok: the MCP server's schema is pi's for ${Object.keys(specs).length} specs (typebox from ${entry})`);
}

// --- The MCP server, driven over a pipe.

type Message = { id?: unknown; result?: Record<string, unknown>; error?: { code: number; message: string } };

// Every server started, killed at the end if a failure left it running,
// which would otherwise keep the script from ending.
const servers: ReturnType<typeof spawn>[] = [];

function start(specFile: string) {
	const child = spawn(process.execPath, [...process.execArgv, join(worker, "submit-output-mcp.ts")], {
		env: { ...process.env, FLOW_OUTPUT_SPEC: specFile },
		stdio: ["pipe", "pipe", "pipe"],
	});
	servers.push(child);
	let stderr = "";
	child.stderr.setEncoding("utf8").on("data", (d: string) => (stderr += d));
	const lines: string[] = [];
	let waiting: ((line: string) => void) | null = null;
	createInterface({ input: child.stdout }).on("line", (line) => {
		if (waiting) {
			const take = waiting;
			waiting = null;
			take(line);
		} else lines.push(line);
	});
	// Once it exited and its stdout and stderr ended, so every line it wrote
	// has been read.
	const exited = new Promise<number | null>((done) => child.on("close", (code) => done(code)));
	const write = (line: string) => child.stdin.write(`${line}\n`);
	// The next line it writes, which has to be a JSON-RPC message.
	const next = () =>
		new Promise<Message>((done, failed) => {
			const timer = setTimeout(() => {
				waiting = null;
				failed(new Error(`the server didn't answer within 5s; its stderr: ${stderr}`));
			}, 5000);
			const take = (line: string) => {
				clearTimeout(timer);
				try {
					const m = JSON.parse(line);
					if (m.jsonrpc !== "2.0") throw new Error("not JSON-RPC 2.0");
					done(m);
				} catch (err) {
					failed(new Error(`the server wrote ${JSON.stringify(line)} on stdout: ${(err as Error).message}`));
				}
			};
			const line = lines.shift();
			if (line !== undefined) take(line);
			else waiting = take;
		});
	let id = 0;
	return {
		write,
		next,
		notify: (method: string) => write(JSON.stringify({ jsonrpc: "2.0", method })),
		request: (method: string, params?: unknown, given: unknown = ++id) => {
			write(JSON.stringify({ jsonrpc: "2.0", id: given, method, ...(params === undefined ? {} : { params }) }));
			return next();
		},
		call: (args: unknown, name = "submit_output") => {
			write(JSON.stringify({ jsonrpc: "2.0", id: ++id, method: "tools/call", params: { name, arguments: args } }));
			return next();
		},
		// Ends its stdin, and says how it exited and what it wrote meanwhile.
		async close(): Promise<{ code: number | null | "still running"; left: string[]; stderr: string }> {
			child.stdin.end();
			let timer: ReturnType<typeof setTimeout> | undefined;
			const code = await Promise.race([exited, new Promise<"still running">((done) => (timer = setTimeout(() => done("still running"), 5000)))]);
			clearTimeout(timer);
			if (code === "still running") child.kill();
			return { code, left: lines, stderr };
		},
	};
}

// What a call answered: its text, as lines, and whether it is an error.
function said(m: Message) {
	const content = m.result?.content as { type: string; text: string }[] | undefined;
	return { lines: content?.map((c) => c.text).join("\n").split("\n"), isError: m.result?.isError === true };
}

async function driveServer() {
	const dir = mkdtempSync(join(tmpdir(), "output-schema-"));
	try {
		const files = join(dir, "files");
		mkdirSync(join(files, "sub"), { recursive: true });
		writeFileSync(join(files, "report.md"), "# Report\n");
		writeFileSync(join(dir, "outside.txt"), "not in the files directory\n");
		await aList(dir, files);
		await anObjectAndDecisions(dir, files);
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
}

// A list of tickets: the session as Claude Code opens it, what the tool
// takes, the problems of bad calls, and good calls writing the output.
async function aList(dir: string, files: string) {
	const spec: Spec = {
		shape: "items",
		objects: [
			{
				name: "Ticket",
				many: true,
				minItems: 1,
				maxItems: 3,
				attributes: [
					attr("key", { type: "text", pattern: "^[A-Z]+-[0-9]+$", description: "The ticket's key" }),
					attr("points", { type: "integer" }),
					attr("due", { type: "datetime" }),
					attr("done", { type: "boolean" }),
					attr("report", { file: true }),
					attr("notes", { many: true }),
					attr("steps", { many: true, attributes: [attr("text", { type: "text" })] }),
				],
			},
		],
		filesDir: files,
		resultFile: join(dir, "list.json"),
	};
	const specFile = join(dir, "list-spec.json");
	writeFileSync(specFile, JSON.stringify(spec));
	const s = start(specFile);

	const probe = await s.request("server/discover", {}, "server-discover-probe-1");
	expect("server/discover", probe, { jsonrpc: "2.0", id: "server-discover-probe-1", error: { code: -32601, message: "Method not found" } });
	const init = await s.request("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "claude-code", version: "2" } });
	expect("initialize", init.result, { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "flow-output", version: "1" } });
	s.notify("notifications/initialized");
	const tools = await s.request("tools/list", {});
	expect("tools/list", tools.result, { tools: [{ name: "submit_output", description: toolDescription, inputSchema: jsonSchema(spec) }] });

	const item = { key: "AB-1", points: 1, due: "2026-11-03", done: true, report: "report.md", notes: [], steps: [] };
	let m = await s.call({
		items: [{ key: 5, points: 1.5, done: "yes", report: "report.md", notes: "n", steps: [{ text: "a" }, {}], colour: "red" }, item, item, item],
		extra: 1,
	});
	expect("a call of the wrong shape", said(m), {
		lines: [
			`"items" must have at most 3 items`,
			`Ticket item 1 "due" is required`,
			`Ticket item 1 "key" must be text`,
			`Ticket item 1 "points" must be an integer`,
			`Ticket item 1 "done" must be true or false`,
			`Ticket item 1 "notes" must be a list`,
			`Ticket item 1 "steps" item 2 "text" is required`,
			`Ticket item 1 "colour" isn't expected; leave it out`,
			`"extra" isn't expected; leave it out`,
		],
		isError: true,
	});
	m = await s.call({ items: [] });
	expect("an empty list", said(m), { lines: [`"items" must have at least 1 item`], isError: true });
	m = await s.call("tickets");
	expect("arguments that aren't an object", said(m), { lines: ["the arguments must be a JSON object"], isError: true });

	m = await s.call({
		items: [
			{ ...item, key: "abc", due: "3 November", report: "../outside.txt" },
			{ ...item, due: "2026-11-03T09:00:00Z", report: "missing.md" },
			{ ...item, report: "sub" },
		],
	});
	expect("a call with bad files, patterns and dates", said(m), {
		lines: [
			`Ticket item 1 "report": ../outside.txt isn't in ${files}`,
			`Ticket item 2 "report": missing.md doesn't exist`,
			`Ticket item 3 "report": sub isn't a file`,
			`Ticket item 1 "key": "abc" doesn't match the regular expression ^[A-Z]+-[0-9]+$`,
			`Ticket item 1 "due" must be a date such as 2026-11-03, or a date and time in RFC 3339 such as 2026-11-03T09:00:00Z`,
		],
		isError: true,
	});
	if (existsSync(spec.resultFile)) fail("a call with problems wrote the output");

	const good = { key: "AB-12", points: 3, due: "2026-11-03T09:00:00+01:00", done: false, report: "report.md", notes: ["x", 2], steps: [{ text: "Write it" }] };
	m = await s.call({ items: [good] });
	expect("a good call", said(m), { lines: ["Output submitted. You are done; reply with one short line and stop."], isError: false });
	expect("what a good call wrote", readFileSync(spec.resultFile, "utf8"), JSON.stringify([{ ...good, report: resolve(files, "report.md") }]));
	m = await s.call({ items: [{ ...good, key: "CD-34" }, { ...good, report: join(files, "report.md") }] });
	expect("a later good call", said(m).isError, false);
	expect(
		"what a later good call wrote over it",
		readFileSync(spec.resultFile, "utf8"),
		JSON.stringify([
			{ ...good, key: "CD-34", report: resolve(files, "report.md") },
			{ ...good, report: resolve(files, "report.md") },
		]),
	);

	const { code, left, stderr } = await s.close();
	expect("how it exited when stdin ended", code, 0);
	expect("what it wrote unasked", left, []);
	if (stderr) console.log(`the server's stderr: ${stderr}`);
}

// An object and a decision on a pull request: an unknown protocol version,
// notifications, an unknown tool and method, lines that aren't requests,
// and decisions.
async function anObjectAndDecisions(dir: string, files: string) {
	const spec: Spec = {
		shape: "object",
		objects: [{ name: "Review", many: false, attributes: [attr("summary", { type: "text" })] }],
		decisions: [review],
		filesDir: files,
		resultFile: join(dir, "review.json"),
	};
	const specFile = join(dir, "review-spec.json");
	writeFileSync(specFile, JSON.stringify(spec));
	const s = start(specFile);

	const init = await s.request("initialize", { protocolVersion: "1999-01-01", capabilities: {} }, "init");
	expect("initialize with a version it doesn't speak", init, {
		jsonrpc: "2.0",
		id: "init",
		result: { protocolVersion: "2025-11-25", capabilities: { tools: {} }, serverInfo: { name: "flow-output", version: "1" } },
	});
	s.notify("notifications/initialized");
	s.notify("notifications/cancelled");
	expect("ping after notifications", await s.request("ping", undefined, 41), { jsonrpc: "2.0", id: 41, result: {} });
	expect("an unknown method", await s.request("resources/list", {}, 7), { jsonrpc: "2.0", id: 7, error: { code: -32601, message: "Method not found" } });
	s.write("{not json");
	expect("a line that isn't JSON", await s.next(), { jsonrpc: "2.0", id: null, error: { code: -32700, message: "Parse error" } });
	s.write("[1, 2]");
	expect("a line that isn't a request", await s.next(), { jsonrpc: "2.0", id: null, error: { code: -32600, message: "Invalid Request" } });
	expect("an unknown tool", said(await s.call({}, "other_tool")), { lines: [`There is no tool "other_tool"; the only one is submit_output.`], isError: true });

	expect("a call without output or decisions", said(await s.call({})), { lines: [`"output" is required`, `"decisions" is required`], isError: true });
	const m = await s.call({ output: { summary: "s", extra: 1 }, decisions: { "Pull request": { state: "merged" }, Bug: { state: "open" } } });
	expect("a call with bad decisions", said(m), {
		lines: [
			`Review "extra" isn't expected; leave it out`,
			`the decision on Pull request "state" must be one of "approved", "changes requested"`,
			`the decision on Bug isn't expected; leave it out`,
		],
		isError: true,
	});
	const good = { output: { summary: "Fine" }, decisions: { "Pull request": { state: "changes requested", instructions: "Rename it" } } };
	expect("a good call with decisions", said(await s.call(good)).isError, false);
	expect("what it wrote with decisions", readFileSync(spec.resultFile, "utf8"), JSON.stringify(good));

	const { code, left } = await s.close();
	expect("how it exited when stdin ended", code, 0);
	expect("what it wrote unasked", left, []);
}

try {
	await compareSchemas();
	await driveServer();
	if (failures === 0) console.log("ok: the MCP server answers a session, checks calls and writes the output");
} catch (err) {
	fail((err as Error).stack ?? String(err));
} finally {
	for (const s of servers) if (s.exitCode === null && s.signalCode === null) s.kill();
}
if (failures > 0) {
	console.error(`${failures} failed`);
	process.exitCode = 1;
}
