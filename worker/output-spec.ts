// The interface of what an agent's process generates, as the JSON file
// FLOW_OUTPUT_SPEC names describes it, and what is checked of what the agent
// hands in for it: shared by pi's extension (submit-output.ts), which builds
// its tool's parameters with typebox, and the MCP server Claude Code gets the
// same tool from (submit-output-mcp.ts), which builds them as plain JSON
// Schema here (jsonSchema) and must build the very same schema; the worker's
// scripts/output-schema.ts checks that they do. Only node: modules are
// imported, so both pi, through jiti, and Node, stripping the types, load it
// as it is.
//
// Each object is a JSON object with its attributes, or {"data": ...} when it
// has none; a list is an array of them. An attribute that is an object of its
// own, or a part, holds a JSON object with its attributes in turn, or for a
// list an array of them. A file attribute holds the path of a file the agent
// wrote in the run's files directory. A typed attribute holds a value of its
// type: an integer, number or yes or no as the parameters say, text matching
// its pattern and a date or date and time as the call is checked, so the
// agent fixes it there; the webapp checks them all again.
//
// An agent deciding the state of objects hands in its output under output,
// next to decisions: for each object it decides on, the state, one of those
// listed, and maybe instructions for the process taking it in that state.
// Deciding only on objects it reviews, it hands in only decisions.

import { realpathSync, statSync } from "node:fs";
import { isAbsolute, relative, resolve } from "node:path";

// How many items a list holds: at least minItems, and at most maxItems if given.
export type Bounds = { minItems?: number; maxItems?: number };

// With attributes of its own, it holds a JSON object of them, or for many
// an array of such objects; without, any JSON value, or for many an array.
export type Attribute = Bounds & {
	name: string;
	description: string;
	file: boolean;
	// What it holds, if typed: text, markdown, integer, number, boolean or
	// datetime; for text, maybe a pattern, anchored, that the whole of it
	// matches. Markdown is text, its description saying to write Markdown;
	// no contentMediaType, which not every model's provider takes.
	type?: string;
	pattern?: string;
	many?: boolean;
	attributes?: Attribute[];
};
export type OutputObject = Bounds & { name: string; many: boolean; attributes: Attribute[] };

// An object whose state the agent decides; one it reviews keeps what it holds.
export type Decision = { name: string; states: string[]; review: boolean };

export type Spec = {
	// "object": one object, the arguments are its attributes. "items": one
	// list, under "items" since tool arguments must be an object. "objects":
	// several, each under its name. "decisions": none, only decisions.
	shape: "object" | "items" | "objects" | "decisions";
	objects: OutputObject[];
	decisions?: Decision[];
	// Where the agent writes files; file attributes must point inside it.
	filesDir: string;
	// Where the output goes once a call passes.
	resultFile: string;
};

// What the tool says it is for, the same to both agents.
export const toolDescription =
	"Hand in what you generate. Its parameters are exactly the objects you generate; call it once as your last action, and only once the work is finished.";

// The output as the webapp reads it: the object, the list, or the objects by
// name; with decisions, a JSON object of the output and the decisions.
export function output(spec: Spec, params: Record<string, unknown>): unknown {
	if (spec.decisions?.length) return params;
	return spec.shape === "items" ? params.items : params;
}

// What the call says each object holds, by object, as a list of items.
export function parts(spec: Spec, args: Record<string, unknown>): [OutputObject, unknown[]][] {
	if (spec.shape === "decisions") return [];
	let params = args;
	if (spec.decisions?.length) {
		if (spec.shape === "items") return [[spec.objects[0], args.output as unknown[]]];
		params = args.output as Record<string, unknown>;
	}
	if (spec.shape === "object") return [[spec.objects[0], [params]]];
	if (spec.shape === "items") return [[spec.objects[0], params.items as unknown[]]];
	return spec.objects.map((o) => [o, o.many ? (params[o.name] as unknown[]) : [params[o.name]]]);
}

// A date, or a date and time in RFC 3339, as the webapp takes them.
export const datetime = /^\d{4}-\d{2}-\d{2}([Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2}))?$/;

// The pattern as JavaScript reads it, or null if it can't: the webapp's is
// Go's, which mostly reads the same; one it can't is left to the webapp.
export function regExp(pattern: string): RegExp | null {
	try {
		return new RegExp(pattern);
	} catch {
		return null;
	}
}

// Says what is wrong with each text attribute not matching its pattern and
// each date and time not written as one, at any depth.
export function typeProblems(spec: Spec, params: Record<string, unknown>): string[] {
	const problems: string[] = [];
	const check = (attributes: Attribute[], items: unknown[], at: string, many: boolean) => {
		for (const a of attributes) {
			items.forEach((item, i) => {
				const where = `${at}${many ? ` item ${i + 1}` : ""} "${a.name}"`;
				const value = (item as Record<string, unknown>)?.[a.name];
				if (a.attributes?.length) {
					if (value !== undefined) check(a.attributes, a.many ? (Array.isArray(value) ? value : []) : [value], where, Boolean(a.many));
					return;
				}
				if (typeof value !== "string") return;
				if (a.type === "datetime" && !datetime.test(value)) {
					problems.push(`${where} must be a date such as 2026-11-03, or a date and time in RFC 3339 such as 2026-11-03T09:00:00Z`);
				} else if (a.type === "text" && a.pattern && regExp(a.pattern)?.test(value) === false) {
					problems.push(`${where}: ${JSON.stringify(value)} doesn't match the regular expression ${a.pattern}`);
				}
			});
		}
	};
	for (const [o, items] of parts(spec, params)) check(o.attributes, items, o.name, o.many);
	return problems;
}

// Says what is wrong with each file attribute that doesn't name a file in
// the files directory, at any depth, and makes the paths of those that do
// absolute.
export function fileProblems(spec: Spec, params: Record<string, unknown>): string[] {
	const root = realpathSync(spec.filesDir);
	const problems: string[] = [];
	const check = (attributes: Attribute[], items: unknown[], at: string, many: boolean) => {
		for (const a of attributes) {
			items.forEach((item, i) => {
				const where = `${at}${many ? ` item ${i + 1}` : ""} "${a.name}"`;
				const value = (item as Record<string, unknown>)?.[a.name];
				if (!a.file) {
					if (a.attributes?.length && value !== undefined) {
						check(a.attributes, a.many ? (Array.isArray(value) ? value : []) : [value], where, Boolean(a.many));
					}
					return;
				}
				if (typeof value !== "string" || value === "") {
					problems.push(`${where} must be the path of a file you wrote in ${spec.filesDir}`);
					return;
				}
				const given = resolve(spec.filesDir, value);
				let path: string;
				try {
					path = realpathSync(given);
				} catch {
					problems.push(`${where}: ${value} doesn't exist`);
					return;
				}
				const rel = relative(root, path);
				if (rel === "" || rel.startsWith("..") || isAbsolute(rel)) {
					problems.push(`${where}: ${value} isn't in ${spec.filesDir}`);
				} else if (!statSync(path).isFile()) {
					problems.push(`${where}: ${value} isn't a file`);
				} else {
					(item as Record<string, unknown>)[a.name] = given;
				}
			});
		}
	};
	for (const [o, items] of parts(spec, params)) check(o.attributes, items, o.name, o.many);
	return problems;
}

// The tool's parameters as plain JSON Schema, the same schema, key for key,
// as submit-output.ts builds with typebox (toolParameters), each function
// here standing for the one of the same name there: every property of an
// object is required but a decision's instructions, an empty description is
// left out, and a list's minItems is given only when above 0.
export type JSONSchema = { [key: string]: unknown };

const fieldsSchema = (attributes: Attribute[], options: { description?: string } = {}): JSONSchema =>
	objectOf(Object.fromEntries(attributes.map((a) => [a.name, attributeSchema(a)])), [], options.description);

// An object of the properties given, all of them required but those named
// optional, and nothing else, in the order typebox writes its keys.
function objectOf(properties: Record<string, JSONSchema>, optional: string[], description?: string): JSONSchema {
	const required = Object.keys(properties).filter((k) => !optional.includes(k));
	return {
		type: "object",
		...(required.length ? { required } : {}),
		properties,
		...(description ? { description } : {}),
		additionalProperties: false,
	};
}

const arrayOf = (items: JSONSchema, b: Bounds, description?: string): JSONSchema => ({
	type: "array",
	items,
	...(description ? { description } : {}),
	...(b.minItems ? { minItems: b.minItems } : {}),
	...(b.maxItems !== undefined ? { maxItems: b.maxItems } : {}),
});

const typed = (type: string, description?: string): JSONSchema => ({ type, ...(description ? { description } : {}) });

function attributeSchema(a: Attribute): JSONSchema {
	const description = a.description || undefined;
	if (a.file) return typed("string", description);
	if (a.attributes?.length) {
		return a.many ? arrayOf(fieldsSchema(a.attributes), a, description) : fieldsSchema(a.attributes, { description });
	}
	if (a.many) return arrayOf({}, a, description);
	switch (a.type) {
		case "text":
		case "markdown":
		case "datetime":
			return typed("string", description);
		case "integer":
			return typed("integer", description);
		case "number":
			return typed("number", description);
		case "boolean":
			return typed("boolean", description);
	}
	return description ? { description } : {};
}

const objectSchema = (o: OutputObject) => fieldsSchema(o.attributes);

const listSchema = (o: OutputObject) => arrayOf(objectSchema(o), o);

const partSchema = (o: OutputObject): JSONSchema => (o.many ? listSchema(o) : objectSchema(o));

const decisionSchema = (d: Decision): JSONSchema =>
	objectOf(
		{
			state: {
				anyOf: d.states.map((s) => ({ type: "string", const: s })),
				description: `The state you decide ${JSON.stringify(d.name)} is in`,
			},
			instructions: typed("string", "What the process taking it in that state is to do, such as what to change"),
		},
		["instructions"],
	);

const decisionsSchema = (ds: Decision[]): JSONSchema =>
	objectOf(Object.fromEntries(ds.map((d) => [d.name, decisionSchema(d)])), []);

// The output alone, as the webapp reads it: under output it needn't be an
// object, so a single list is the array itself.
function outputSchema(spec: Spec): JSONSchema {
	return spec.shape === "items" ? listSchema(spec.objects[0]) : parameters(spec);
}

export function jsonSchema(spec: Spec): JSONSchema {
	if (!spec.decisions?.length) return parameters(spec);
	if (spec.shape === "decisions") return objectOf({ decisions: decisionsSchema(spec.decisions) }, []);
	return objectOf({ output: outputSchema(spec), decisions: decisionsSchema(spec.decisions) }, []);
}

function parameters(spec: Spec): JSONSchema {
	const [first] = spec.objects;
	switch (spec.shape) {
		case "object":
			return objectSchema(first);
		case "items":
			return objectOf({ items: listSchema(first) }, []);
		default:
			return objectOf(Object.fromEntries(spec.objects.map((o) => [o.name, partSchema(o)])), []);
	}
}
