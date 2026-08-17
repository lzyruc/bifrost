/**
 * SSE frame parsing for Warp, kept separate from the React hook so it can be
 * tested without a DOM or a network.
 */

export type WarpEventType = "start" | "delta" | "tool_call_start" | "tool_call_end" | "error" | "done";

export interface WarpEvent {
	type: WarpEventType;
	delta?: string;
	tool_id?: string;
	tool_name?: string;
	arguments?: string;
	iteration?: number;
	duration_ms?: number;
	failed?: boolean;
	code?: string;
	message?: string;
	finish_reason?: string;
	iterations?: number;
	model?: string;
	provider?: string;
}

/**
 * Splits a byte-stream buffer into complete SSE frames.
 *
 * Returns the frames it could complete plus whatever is left over, because a
 * chunk boundary can land mid-frame. Feeding the remainder back in on the next
 * read is what stops a delta from being silently dropped when the network splits
 * a message in an inconvenient place.
 */
export function splitWarpFrames(buffer: string): { frames: string[]; rest: string } {
	// Normalise line endings first. The SSE spec allows CRLF and lone CR, and a
	// proxy that rewrites them is entirely legal - but splitting on "\n\n" alone
	// then finds no frame boundary at all, so the whole answer is silently
	// dropped and the chat completes empty with nothing to explain it.
	const parts = buffer.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n\n");
	// The final part has no terminator yet, so it may be incomplete.
	const rest = parts.pop() ?? "";
	return { frames: parts.filter((part) => part.trim() !== ""), rest };
}

/**
 * Parses one SSE frame into an event.
 *
 * The `event:` line is ignored in favour of the `type` field inside the JSON.
 * They always agree, and trusting the payload means one source of truth rather
 * than two that can drift.
 *
 * Returns null for anything unparseable - heartbeat comments, blank frames, a
 * truncated write - so the caller can skip rather than tear down a stream that
 * is otherwise healthy.
 */
export function parseWarpFrame(frame: string): WarpEvent | null {
	// The space after `data:` is optional in the SSE spec, so both forms have to
	// be accepted; only one leading space is consumed, because any further
	// whitespace is part of the value.
	const dataLines = frame
		.replace(/\r\n/g, "\n")
		.replace(/\r/g, "\n")
		.split("\n")
		.filter((line) => line.startsWith("data:"))
		.map((line) => {
			const value = line.slice(5);
			return value.startsWith(" ") ? value.slice(1) : value;
		});
	if (dataLines.length === 0) return null;

	const payload = dataLines.join("\n");
	if (payload === "[DONE]") return null;

	try {
		const parsed = JSON.parse(payload) as WarpEvent;
		return parsed.type ? parsed : null;
	} catch {
		return null;
	}
}

/**
 * Human-readable label for a tool, used on the collapsed row in the transcript.
 *
 * Falling back to the raw name keeps a newly added server-side tool legible
 * instead of rendering as blank until the UI catches up.
 */
export function warpToolLabel(name: string): string {
	const labels: Record<string, string> = {
		query_logs: "Searched request logs",
		get_log_detail: "Opened a request",
		query_metrics: "Queried metrics",
		query_user_usage: "Ranked users by usage",
		query_virtual_key_usage: "Ranked virtual keys by usage",
		query_model_performance: "Compared models and providers",
		describe_filter_space: "Checked available values",
	};
	return labels[name] ?? name;
}

/**
 * Message shown for a terminal error code.
 *
 * `max_iterations` and `timeout` are phrased as something the user can act on,
 * because they usually mean the question was too broad rather than that
 * anything is broken.
 */
export function errorMessage(code: string | undefined, message: string | undefined): string {
	switch (code) {
		case "not_configured":
			return "Warp is not configured yet.";
		case "max_iterations":
			return "Warp could not settle on an answer. Try a narrower question.";
		case "timeout":
			return "That took too long. Try a shorter time range.";
		case "cancelled":
			return "Stopped.";
		default:
			return message || "Something went wrong.";
	}
}
/**
 * Encodes a turn's terminal error as the `code:message` pair the transcript
 * decodes.
 *
 * The leading colon on a code-less error is load-bearing. Without it the
 * decoder reads the entire message as a code, finds no match, and falls through
 * to the generic "Something went wrong." - throwing away the status line or
 * network error that was the only useful part.
 */
export function encodeTurnError(code: string | undefined, message: string): string {
	return `${code ?? ""}:${message}`;
}

/**
 * Splits an encoded turn error back into its parts.
 *
 * Splits on the first colon only, so a message that contains colons of its own
 * ("connect: connection refused") survives intact. A string with no colon is
 * treated as a bare message rather than a code, which keeps errors produced
 * before this encoding existed readable.
 */
export function decodeTurnError(error: string): { code: string; message: string } {
	const separator = error.indexOf(":");
	if (separator === -1) return { code: "", message: error.trim() };
	return { code: error.slice(0, separator).trim(), message: error.slice(separator + 1).trim() };
}