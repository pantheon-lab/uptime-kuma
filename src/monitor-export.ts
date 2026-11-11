// Writing monitors to a file that the Import dialog reads back: YAML or JSON, a list of monitors in
// the REST API's format, as the server's exportMonitors returns them.

export type ExportFormat = "yaml" | "json";

/**
 * The file's contents
 * @param monitors Monitors from exportMonitors
 * @param format YAML or JSON
 * @param dump A YAML writer, such as js-yaml's dump
 * @returns The text
 */
export function monitorsToFile(
    monitors: Record<string, unknown>[],
    format: ExportFormat,
    dump: (value: unknown) => string
): string {
    if (format === "json") {
        return JSON.stringify(monitors, null, 2) + "\n";
    }
    return dump(monitors);
}

/**
 * The file's name, such as monitors-2026-11-10.yaml
 * @param format YAML or JSON
 * @param now When it's exported
 * @returns The name
 */
export function exportFileName(format: ExportFormat, now: Date): string {
    const pad = (n: number) => String(n).padStart(2, "0");
    const day = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
    return `monitors-${day}.${format}`;
}
