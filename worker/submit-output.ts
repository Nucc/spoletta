// Makes an agent hand in what it generates through the submit_output tool,
// whose parameters are the interface of the process's output objects: pi
// checks every call against them and gives the agent the error to fix, and
// the arguments of the call that passes are the run's output. The worker
// loads it for a run that generates objects, describing them in the JSON
// file FLOW_OUTPUT_SPEC names.
//
// What the spec holds, how each object is handed in and what is checked of
// it beyond the parameters (files, patterns, dates) is in output-spec.ts,
// which Claude Code's MCP server for the same tool (submit-output-mcp.ts)
// shares; the parameters are built here with typebox, and there as the same
// schema in plain JSON Schema. The import names output-spec.ts with its
// extension, which jiti, loading this for pi, resolves as it is, and which
// Node needs when it loads it stripping the types, as the worker's
// scripts/output-schema.ts does.

import { readFileSync, writeFileSync } from "node:fs";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type, type TSchema } from "typebox";
import { fileProblems, output, toolDescription, typeProblems } from "./output-spec.ts";
import type { Attribute, Bounds, Decision, OutputObject, Spec } from "./output-spec.ts";

const fieldsSchema = (attributes: Attribute[], options: { description?: string } = {}): TSchema =>
	Type.Object(Object.fromEntries(attributes.map((a) => [a.name, attributeSchema(a)])), {
		...options,
		additionalProperties: false,
	});

const bounds = (b: Bounds): Bounds => ({
	...(b.minItems ? { minItems: b.minItems } : {}),
	...(b.maxItems !== undefined ? { maxItems: b.maxItems } : {}),
});

function attributeSchema(a: Attribute): TSchema {
	const description = a.description || undefined;
	if (a.file) return Type.String({ description });
	if (a.attributes?.length) {
		return a.many
			? Type.Array(fieldsSchema(a.attributes), { description, ...bounds(a) })
			: fieldsSchema(a.attributes, { description });
	}
	if (a.many) return Type.Array(Type.Any(), { description, ...bounds(a) });
	switch (a.type) {
		case "text":
		case "markdown":
		case "datetime":
			return Type.String({ description });
		case "integer":
			return Type.Integer({ description });
		case "number":
			return Type.Number({ description });
		case "boolean":
			return Type.Boolean({ description });
	}
	return Type.Any({ description });
}

const objectSchema = (o: OutputObject) => fieldsSchema(o.attributes);

const listSchema = (o: OutputObject) => Type.Array(objectSchema(o), bounds(o));

const partSchema = (o: OutputObject): TSchema => (o.many ? listSchema(o) : objectSchema(o));

const decisionSchema = (d: Decision): TSchema =>
	Type.Object(
		{
			state: Type.Union(
				d.states.map((s) => Type.Literal(s)),
				{ description: `The state you decide ${JSON.stringify(d.name)} is in` },
			),
			instructions: Type.Optional(
				Type.String({ description: "What the process taking it in that state is to do, such as what to change" }),
			),
		},
		{ additionalProperties: false },
	);

const decisionsSchema = (ds: Decision[]): TSchema =>
	Type.Object(Object.fromEntries(ds.map((d) => [d.name, decisionSchema(d)])), { additionalProperties: false });

// The output alone, as the webapp reads it: under output it needn't be an
// object, so a single list is the array itself.
function outputSchema(spec: Spec): TSchema {
	return spec.shape === "items" ? listSchema(spec.objects[0]) : parameters(spec);
}

// Exported for scripts/output-schema.ts, which checks that output-spec.ts's
// jsonSchema builds the same; pi takes only the default export.
export function toolParameters(spec: Spec): TSchema {
	if (!spec.decisions?.length) return parameters(spec);
	if (spec.shape === "decisions") {
		return Type.Object({ decisions: decisionsSchema(spec.decisions) }, { additionalProperties: false });
	}
	return Type.Object({ output: outputSchema(spec), decisions: decisionsSchema(spec.decisions) }, { additionalProperties: false });
}

function parameters(spec: Spec): TSchema {
	const [first] = spec.objects;
	switch (spec.shape) {
		case "object":
			return objectSchema(first);
		case "items":
			return Type.Object({ items: listSchema(first) }, { additionalProperties: false });
		default:
			return Type.Object(Object.fromEntries(spec.objects.map((o) => [o.name, partSchema(o)])), {
				additionalProperties: false,
			});
	}
}

export default function (pi: ExtensionAPI) {
	const specFile = process.env.FLOW_OUTPUT_SPEC;
	if (!specFile) return;
	const spec: Spec = JSON.parse(readFileSync(specFile, "utf8"));

	pi.registerTool({
		name: "submit_output",
		label: "Submit output",
		description: toolDescription,
		promptSnippet: "Hand in the objects you generate; the only way to give your result",
		promptGuidelines: [
			"Give your result only by calling submit_output; text you write as a reply is not your result.",
			"If submit_output reports a problem, fix it and call submit_output again.",
		],
		parameters: toolParameters(spec),
		async execute(_toolCallId, params) {
			const args = params as Record<string, unknown>;
			const problems = [...fileProblems(spec, args), ...typeProblems(spec, args)];
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
