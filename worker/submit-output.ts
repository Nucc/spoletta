// Makes an agent hand in what it generates through the submit_output tool,
// whose parameters are the interface of the process's output objects: pi
// checks every call against them and gives the agent the error to fix, and
// the arguments of the call that passes are the run's output. The worker
// loads it for a run that generates objects, describing them in the JSON
// file FLOW_OUTPUT_SPEC names.
//
// Each object is a JSON object with its attributes, or {"data": ...} when it
// has none; a list is an array of them. A file attribute holds the path of a
// file the agent wrote in the run's files directory.

import { readFileSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { isAbsolute, relative, resolve } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type, type TSchema } from "typebox";

type Attribute = { name: string; description: string; file: boolean };
type OutputObject = { name: string; many: boolean; attributes: Attribute[] };

type Spec = {
	// "object": one object, the arguments are its attributes. "items": one
	// list, under "items" since tool arguments must be an object. "objects":
	// several, each under its name.
	shape: "object" | "items" | "objects";
	objects: OutputObject[];
	// Where the agent writes files; file attributes must point inside it.
	filesDir: string;
	// Where the output goes once a call passes.
	resultFile: string;
};

const objectSchema = (o: OutputObject) =>
	Type.Object(
		Object.fromEntries(
			o.attributes.map((a) => [
				a.name,
				a.file ? Type.String({ description: a.description }) : Type.Any({ description: a.description }),
			]),
		),
		{ additionalProperties: false },
	);

const partSchema = (o: OutputObject): TSchema => (o.many ? Type.Array(objectSchema(o)) : objectSchema(o));

function parameters(spec: Spec): TSchema {
	const [first] = spec.objects;
	switch (spec.shape) {
		case "object":
			return objectSchema(first);
		case "items":
			return Type.Object({ items: Type.Array(objectSchema(first)) }, { additionalProperties: false });
		default:
			return Type.Object(Object.fromEntries(spec.objects.map((o) => [o.name, partSchema(o)])), {
				additionalProperties: false,
			});
	}
}

// The output as the webapp reads it: the object, the list, or the objects by name.
function output(spec: Spec, params: Record<string, unknown>): unknown {
	return spec.shape === "items" ? params.items : params;
}

// What the call says each object holds, by object, as a list of items.
function parts(spec: Spec, params: Record<string, unknown>): [OutputObject, unknown[]][] {
	if (spec.shape === "object") return [[spec.objects[0], [params]]];
	if (spec.shape === "items") return [[spec.objects[0], params.items as unknown[]]];
	return spec.objects.map((o) => [o, o.many ? (params[o.name] as unknown[]) : [params[o.name]]]);
}

// Says what is wrong with each file attribute that doesn't name a file in
// the files directory, and makes the paths of those that do absolute.
function fileProblems(spec: Spec, params: Record<string, unknown>): string[] {
	const root = realpathSync(spec.filesDir);
	const problems: string[] = [];
	for (const [o, items] of parts(spec, params)) {
		for (const a of o.attributes.filter((a) => a.file)) {
			items.forEach((item, i) => {
				const where = `${o.name}${o.many ? ` item ${i + 1}` : ""} "${a.name}"`;
				const value = (item as Record<string, unknown>)[a.name];
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
	}
	return problems;
}

export default function (pi: ExtensionAPI) {
	const specFile = process.env.FLOW_OUTPUT_SPEC;
	if (!specFile) return;
	const spec: Spec = JSON.parse(readFileSync(specFile, "utf8"));

	pi.registerTool({
		name: "submit_output",
		label: "Submit output",
		description:
			"Hand in what you generate. Its parameters are exactly the objects you generate; call it once as your last action, and only once the work is finished.",
		promptSnippet: "Hand in the objects you generate; the only way to give your result",
		promptGuidelines: [
			"Give your result only by calling submit_output; text you write as a reply is not your result.",
			"If submit_output reports a problem, fix it and call submit_output again.",
		],
		parameters: parameters(spec),
		async execute(_toolCallId, params) {
			const args = params as Record<string, unknown>;
			const problems = fileProblems(spec, args);
			if (problems.length > 0) throw new Error(problems.join("\n"));
			writeFileSync(spec.resultFile, JSON.stringify(output(spec, args)));
			return {
				content: [{ type: "text", text: "Output submitted." }],
				details: undefined,
				terminate: true,
			};
		},
	});
}
