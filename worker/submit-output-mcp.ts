// Gives Claude Code the submit_output tool pi gets from submit-output.ts, as
// an MCP server on stdio: Claude Code starts it with `node
// /opt/flow/submit-output-mcp.ts`, as the mcp.json the worker writes for its
// step says, inside the step's sandbox, and it reads the interface of what
// the process generates from the JSON file FLOW_OUTPUT_SPEC names, the same
// file pi's extension reads. Node runs it as it is, stripping the types; it
// needs nothing but Node and output-spec.ts beside it.
//
// The tool's parameters are the schema pi's are, written as plain JSON Schema
// (jsonSchema in output-spec.ts). Claude Code hands the server whatever the
// agent called the tool with, unchecked, so the server checks the call
// against that schema itself (check), naming what is wrong in the board's
// words as output-spec.ts's own checks do, then checks files, patterns and
// dates as pi's extension does. A call with problems is answered with them,
// as an error the agent sees and fixes; one without writes the output where
// the worker reads it, as pi's extension does, and a later call writes it
// over.
//
// It speaks JSON-RPC 2.0, a message a line, on stdin and stdout: stdout
// carries nothing but its answers, anything else goes to stderr. It answers
// initialize, ping, tools/list and tools/call, takes notifications without a
// word, answers any other request that it has no such method (as Claude
// Code's own server/discover probe, which it sends first), and ends when
// stdin does.

import { readFileSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { fileProblems, jsonSchema, output, toolDescription, typeProblems } from "./output-spec.ts";
import type { JSONSchema, Spec } from "./output-spec.ts";

// The protocol versions it speaks, the latest first: a client asking for one
// of them gets it, any other the latest, which it may then refuse.
const protocolVersions = ["2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"];

const submitted = "Output submitted. You are done; reply with one short line and stop.";

function readSpec(): Spec {
	const file = process.env.FLOW_OUTPUT_SPEC;
	if (!file) {
		console.error("submit-output-mcp: FLOW_OUTPUT_SPEC isn't set");
		process.exit(1);
	}
	try {
		return JSON.parse(readFileSync(file, "utf8"));
	} catch (err) {
		console.error(`submit-output-mcp: can't read the output spec ${file}: ${(err as Error).message}`);
		process.exit(1);
	}
}

const spec = readSpec();
const schema = jsonSchema(spec);

// Where a value is in the call: the keys and list indexes leading to it.
type Path = (string | number)[];

// Each attribute in quotes and each item of a list by its number from 1.
const steps = (p: Path) => p.map((s) => (typeof s === "number" ? ` item ${s + 1}` : ` ${JSON.stringify(s)}`)).join("");

// Where a value is in the call, in the board's words, as typeProblems and
// fileProblems say it: the object's name, then its attributes and items, so
// that items[1].title of a list of tickets is `Ticket item 2 "title"`. A
// parameter wrapping objects (items, output, decisions) is named as it is
// when the problem is with the parameter itself, and so is any other key the
// call has where no object goes; a decision is "the decision on" its object.
function where(path: Path): string {
	if (path.length === 0) return "the arguments";
	const [first] = spec.objects;
	const named = (p: Path) => steps(p).slice(1);
	let p = path;
	if (spec.decisions?.length) {
		if (p[0] === "decisions" && p.length > 1) return `the decision on ${p[1]}${steps(p.slice(2))}`;
		if (p[0] !== "output" || p.length === 1) return named(p);
		p = p.slice(1);
		if (spec.shape === "items") return `${first.name}${steps(p)}`;
	} else if (spec.shape === "items") {
		if (p[0] !== "items" || p.length === 1) return named(p);
		return `${first.name}${steps(p.slice(1))}`;
	}
	if (spec.shape === "object") return `${first.name}${steps(p)}`;
	return spec.objects.some((o) => o.name === p[0]) ? `${p[0]}${steps(p.slice(1))}` : named(p);
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

const plural = (n: number, what: string) => `${n} ${what}${n === 1 ? "" : "s"}`;

// What a value of each JSON Schema type is called when it isn't one.
const typeNames: Record<string, string> = {
	object: "a JSON object",
	array: "a list",
	string: "text",
	integer: "an integer",
	number: "a number",
	boolean: "true or false",
};

const isType: Record<string, (v: unknown) => boolean> = {
	object: isObject,
	array: Array.isArray,
	string: (v) => typeof v === "string",
	integer: (v) => Number.isInteger(v),
	number: (v) => typeof v === "number" && Number.isFinite(v),
	boolean: (v) => typeof v === "boolean",
};

// Says what is wrong with the value against the schema, as far as the
// schemas jsonSchema builds go: objects with their properties, those
// required and no others; lists with their items and how many; text,
// integers, numbers, true or false; a state as one of its constants; and
// any value where no type is given. A value of the wrong type isn't looked
// into further.
function check(schema: JSONSchema, value: unknown, path: Path, problems: string[]): void {
	if (Array.isArray(schema.anyOf)) {
		const options = schema.anyOf as JSONSchema[];
		const fits = (o: JSONSchema) => {
			const found: string[] = [];
			check(o, value, path, found);
			return found.length === 0;
		};
		if (options.some(fits)) return;
		const constants = options.filter((o) => "const" in o).map((o) => JSON.stringify(o.const));
		if (constants.length === 0) problems.push(`${where(path)} has nothing it may be`);
		else if (constants.length === 1) problems.push(`${where(path)} must be ${constants[0]}`);
		else problems.push(`${where(path)} must be one of ${constants.join(", ")}`);
		return;
	}
	const type = schema.type as string | undefined;
	if (type !== undefined && isType[type] && !isType[type](value)) {
		problems.push(`${where(path)} must be ${typeNames[type]}`);
		return;
	}
	if ("const" in schema && value !== schema.const) {
		problems.push(`${where(path)} must be ${JSON.stringify(schema.const)}`);
		return;
	}
	if (type === "object") {
		const fields = value as Record<string, unknown>;
		const properties = (schema.properties ?? {}) as Record<string, JSONSchema>;
		for (const name of (schema.required ?? []) as string[]) {
			if (!Object.hasOwn(fields, name)) problems.push(`${where([...path, name])} is required`);
		}
		for (const [name, v] of Object.entries(fields)) {
			if (Object.hasOwn(properties, name)) check(properties[name], v, [...path, name], problems);
			else if (schema.additionalProperties === false) problems.push(`${where([...path, name])} isn't expected; leave it out`);
		}
	} else if (type === "array") {
		const items = value as unknown[];
		const min = schema.minItems as number | undefined;
		const max = schema.maxItems as number | undefined;
		if (min !== undefined && items.length < min) problems.push(`${where(path)} must have at least ${plural(min, "item")}`);
		if (max !== undefined && items.length > max) problems.push(`${where(path)} must have at most ${plural(max, "item")}`);
		if (schema.items) items.forEach((item, i) => check(schema.items as JSONSchema, item, [...path, i], problems));
	}
}

// What the agent's call of submit_output comes to: its problems, or the
// output written.
function submit(args: unknown): { text: string; isError?: boolean } {
	const problems: string[] = [];
	check(schema, args, [], problems);
	if (problems.length === 0) {
		const params = args as Record<string, unknown>;
		try {
			problems.push(...fileProblems(spec, params), ...typeProblems(spec, params));
			if (problems.length === 0) writeFileSync(spec.resultFile, JSON.stringify(output(spec, params)));
		} catch (err) {
			problems.push(`Couldn't take the output: ${(err as Error).message}`);
		}
	}
	return problems.length > 0 ? { text: problems.join("\n"), isError: true } : { text: submitted };
}

const tool = { name: "submit_output", description: toolDescription, inputSchema: schema };

class RPCError extends Error {
	code: number;
	constructor(code: number, message: string) {
		super(message);
		this.code = code;
	}
}

// The result of a request, or an RPCError for the error to answer with.
function answer(method: string, params: Record<string, unknown>): unknown {
	switch (method) {
		case "initialize": {
			const asked = params.protocolVersion as string;
			return {
				protocolVersion: protocolVersions.includes(asked) ? asked : protocolVersions[0],
				capabilities: { tools: {} },
				serverInfo: { name: "flow-output", version: "1" },
			};
		}
		case "ping":
			return {};
		case "tools/list":
			return { tools: [tool] };
		case "tools/call": {
			if (params.name !== tool.name) {
				return { content: [{ type: "text", text: `There is no tool ${JSON.stringify(params.name)}; the only one is submit_output.` }], isError: true };
			}
			const { text, isError } = submit(params.arguments ?? {});
			return { content: [{ type: "text", text }], ...(isError ? { isError } : {}) };
		}
	}
	throw new RPCError(-32601, "Method not found");
}

const send = (message: Record<string, unknown>) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);

function receive(line: string) {
	if (line.trim() === "") return;
	let message: unknown;
	try {
		message = JSON.parse(line);
	} catch {
		send({ id: null, error: { code: -32700, message: "Parse error" } });
		return;
	}
	if (!isObject(message)) {
		send({ id: null, error: { code: -32600, message: "Invalid Request" } });
		return;
	}
	const { id, method, params } = message;
	const request = Object.hasOwn(message, "id");
	if (typeof method !== "string") {
		// An answer to a request of ours, which it never sends, is left be.
		if (request && !Object.hasOwn(message, "result") && !Object.hasOwn(message, "error")) {
			send({ id, error: { code: -32600, message: "Invalid Request" } });
		}
		return;
	}
	if (!request) return;
	try {
		send({ id, result: answer(method, isObject(params) ? params : {}) });
	} catch (err) {
		if (err instanceof RPCError) send({ id, error: { code: err.code, message: err.message } });
		else send({ id, error: { code: -32603, message: (err as Error).message } });
	}
}

// Claude Code gone, there is nobody to answer.
process.stdout.on("error", () => process.exit(0));
createInterface({ input: process.stdin, crlfDelay: Infinity }).on("line", receive);
