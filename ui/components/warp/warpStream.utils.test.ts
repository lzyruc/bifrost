import { describe, expect, it } from "vitest";
import { decodeTurnError, encodeTurnError, errorMessage, warpToolLabel, parseWarpFrame, splitWarpFrames } from "./warpStream.utils";

describe("splitWarpFrames", () => {
	it("returns complete frames and keeps the remainder", () => {
		const { frames, rest } = splitWarpFrames("event: delta\ndata: {}\n\nevent: done\ndata: {");
		expect(frames).toEqual(["event: delta\ndata: {}"]);
		expect(rest).toBe("event: done\ndata: {");
	});

	// A chunk boundary can land mid-frame. Dropping the remainder instead of
	// carrying it forward loses whatever token was being written at that moment,
	// which reads as a corrupted answer rather than an error.
	it("reassembles a frame split across two reads", () => {
		const first = splitWarpFrames('event: delta\ndata: {"type":"delta","del');
		expect(first.frames).toHaveLength(0);

		const second = splitWarpFrames(first.rest + 'ta":"hello"}\n\n');
		expect(second.frames).toHaveLength(1);
		expect(parseWarpFrame(second.frames[0])?.delta).toBe("hello");
	});

	it("ignores blank frames", () => {
		const { frames } = splitWarpFrames("\n\n\n\ndata: {}\n\n");
		expect(frames).toEqual(["data: {}"]);
	});
});

describe("parseWarpFrame", () => {
	it("parses an event from the data payload", () => {
		const event = parseWarpFrame('event: tool_call_end\ndata: {"type":"tool_call_end","tool_name":"query_metrics","duration_ms":42}');
		expect(event).toMatchObject({ type: "tool_call_end", tool_name: "query_metrics", duration_ms: 42 });
	});

	// Heartbeats keep the connection honest but carry no data. Treating one as a
	// parse failure would tear down a healthy stream.
	it("returns null for a heartbeat comment", () => {
		expect(parseWarpFrame(": heartbeat")).toBeNull();
	});

	it("returns null for malformed JSON rather than throwing", () => {
		expect(parseWarpFrame("data: {not json")).toBeNull();
	});

	it("returns null for the [DONE] sentinel", () => {
		expect(parseWarpFrame("data: [DONE]")).toBeNull();
	});

	it("returns null when the payload has no type", () => {
		expect(parseWarpFrame('data: {"delta":"orphan"}')).toBeNull();
	});
});

describe("warpToolLabel", () => {
	it("maps known tools to readable labels", () => {
		expect(warpToolLabel("query_metrics")).toBe("Queried metrics");
	});

	// A tool added server-side should still render legibly instead of blank.
	it("falls back to the raw name for unknown tools", () => {
		expect(warpToolLabel("query_something_new")).toBe("query_something_new");
	});
});

describe("errorMessage", () => {
	it("phrases max_iterations as something the user can act on", () => {
		expect(errorMessage("max_iterations", "")).toContain("narrower question");
	});

	it("phrases timeout as something the user can act on", () => {
		expect(errorMessage("timeout", "")).toContain("shorter time range");
	});

	it("falls back to the server message for unknown codes", () => {
		expect(errorMessage("something_else", "upstream exploded")).toBe("upstream exploded");
	});

	it("has a message even when the server sends nothing useful", () => {
		expect(errorMessage(undefined, undefined)).toBe("Something went wrong.");
	});
});
// The SSE spec allows CRLF line endings and makes the space after `data:`
// optional. A stream from a proxy that normalises to CRLF, or a server that
// omits the space, parsed to nothing at all - so the chat completed with an
// empty answer and no error to explain it.
describe("SSE wire tolerance", () => {
	it("splits frames delimited by CRLF", () => {
		const { frames, rest } = splitWarpFrames('event: delta\r\ndata: {"type":"delta","delta":"hi"}\r\n\r\nevent: done\r\ndata: {');
		expect(frames).toHaveLength(1);
		expect(parseWarpFrame(frames[0])?.delta).toBe("hi");
		expect(rest).toContain("event: done");
	});

	it("parses a data field with no space after the colon", () => {
		expect(parseWarpFrame('data:{"type":"delta","delta":"hi"}')?.delta).toBe("hi");
	});

	it("parses a CRLF frame whose data field has no space", () => {
		expect(parseWarpFrame('event: delta\r\ndata:{"type":"delta","delta":"hi"}')?.delta).toBe("hi");
	});

	it("still treats [DONE] as a sentinel without the space", () => {
		expect(parseWarpFrame("data:[DONE]")).toBeNull();
	});

	it("keeps multi-line data joined on newlines", () => {
		expect(parseWarpFrame('data:{"type":"delta",\r\ndata:"delta":"hi"}')?.delta).toBe("hi");
	});
});

// The turn error is an encoded `code:message` pair. Producers that emitted a
// bare message with no colon had the whole message read back as a code, which
// matched nothing and rendered the generic "Something went wrong." - losing the
// status line, the network error, and every other detail worth showing.
describe("turn error encoding", () => {
	it("round-trips a coded error", () => {
		const { code, message } = decodeTurnError(encodeTurnError("not_configured", ""));
		expect(code).toBe("not_configured");
		expect(errorMessage(code, message)).toBe("Warp is not configured yet.");
	});

	it("keeps a message that has no code", () => {
		const encoded = encodeTurnError(undefined, "Warp request failed (500)");
		const { code, message } = decodeTurnError(encoded);
		expect(code).toBe("");
		expect(message).toBe("Warp request failed (500)");
		expect(errorMessage(code, message)).toBe("Warp request failed (500)");
	});

	it("keeps colons inside the message intact", () => {
		const { code, message } = decodeTurnError(encodeTurnError(undefined, "connect: connection refused"));
		expect(code).toBe("");
		expect(message).toBe("connect: connection refused");
	});

	it("decodes a legacy bare message as a message, not a code", () => {
		const { code, message } = decodeTurnError("Warp returned no response body");
		expect(code).toBe("");
		expect(message).toBe("Warp returned no response body");
		expect(errorMessage(code, message)).toBe("Warp returned no response body");
	});
});