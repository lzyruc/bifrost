// Bulk rotation returns one previous_value_expires_at per key, each computed from
// its own time.Now() on the server, so the deadlines in one response can differ.

/**
 * Picks the single grace deadline to show for a bulk rotation: the latest
 * deadline across the returned keys, i.e. the time after which no retired
 * value authenticates anymore. Returns null when no key has a grace window.
 */
export function latestGraceDeadline(virtualKeys: { previous_value_expires_at?: string | null }[]): string | null {
	let latest: string | null = null;
	for (const vk of virtualKeys) {
		const deadline = vk.previous_value_expires_at;
		if (deadline && (latest === null || new Date(deadline).getTime() > new Date(latest).getTime())) {
			latest = deadline;
		}
	}
	return latest;
}

/**
 * Renders the "Assigned To" label for a virtual key, or null when it is assigned
 * to nothing. A key is assigned to at most one of a team, a customer, or a user.
 *
 * Shared by the table cell and the CSV export so the two cannot drift: the export
 * used to omit the user branch entirely, which left the column blank for every
 * user-assigned key.
 */
export function assignedToLabel(vk: {
	team?: { name: string };
	customer?: { name: string };
	assigned_user?: { name: string; email: string } | null;
}): string | null {
	if (vk.team) return `Team: ${vk.team.name}`;
	if (vk.customer) return `Customer: ${vk.customer.name}`;
	if (vk.assigned_user) return `User: ${vk.assigned_user.name || vk.assigned_user.email}`;
	return null;
}
