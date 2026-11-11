// A monitor's path, "Group / Subgroup / Monitor", from the monitor list as it is now. The server's
// pathName can lag behind a group renamed or moved meanwhile.

export interface PathMonitor {
    id: number;
    name: string;
    parent: number | null;
}

/**
 * The names from the top group down to the monitor
 * @param list The monitor list, by id
 * @param monitor The monitor
 * @returns The path, with " / " between the names
 */
export function monitorPath(list: Record<string, PathMonitor | undefined>, monitor: PathMonitor): string {
    const names = [monitor.name];
    const seen = new Set([monitor.id]);
    // A group missing from the list ends the path; one already seen means a loop, which ends it too
    for (let p = monitor.parent == null ? undefined : list[monitor.parent]; p && !seen.has(p.id);) {
        seen.add(p.id);
        names.unshift(p.name);
        p = p.parent == null ? undefined : list[p.parent];
    }
    return names.join(" / ");
}
