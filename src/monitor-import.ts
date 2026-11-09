// Reading monitors to import from a CSV or YAML file, into the REST API's monitor format, which
// the server checks field by field. CSV has a header row of field names, one monitor per row, and
// leaves a field at its default when its cell is empty.

export type ImportFormat = "csv" | "yaml";

export interface ParsedImport {
    monitors: Record<string, unknown>[];
    // Problems found before sending, each naming its row
    errors: string[];
}

const NUMBER_FIELDS = new Set([
    "interval",
    "retryInterval",
    "timeout",
    "maxRetries",
    "resendInterval",
    "parentId",
    "maxRedirects",
    "port",
    "pingCount",
    "pingPerRequestTimeout",
    "packetSize",
    "manualStatus",
    "dockerHost",
    "proxyId",
    "remoteBrowser",
    "responseMaxLength",
    "screenshotDelay",
    "ntpStratumThreshold",
    "ntpTimeOffsetThreshold",
    "ntpRootDispersionThreshold",
]);

const BOOLEAN_FIELDS = new Set([
    "active",
    "upsideDown",
    "ignoreTls",
    "expiryNotification",
    "domainExpiryNotification",
    "invertKeyword",
    "propagateTrace",
    "cacheBust",
    "gamedigGivenPortOnly",
    "grpcEnableTls",
    "kafkaProducerAllowAutoTopicCreation",
    "kafkaProducerSsl",
    "retryOnlyOnStatusCodeFailure",
    "saveErrorResponse",
    "saveResponse",
    "wsIgnoreSecWebsocketAcceptHeader",
]);

// Lists, written in one cell with ";" between the values
const LIST_FIELDS = new Set(["acceptedStatusCodes", "kafkaProducerBrokers", "rabbitmqNodes"]);

// Objects, written in one cell as JSON
const JSON_FIELDS = new Set(["headers", "conditions", "kafkaProducerSaslOptions"]);

/**
 * Which format a file is in, from its name or else its first character
 * @param name The file's name, or ""
 * @param text Its contents
 * @returns The format
 */
export function detectFormat(name: string, text: string): ImportFormat {
    const lower = name.toLowerCase();
    if (lower.endsWith(".csv")) {
        return "csv";
    }
    if (lower.endsWith(".yaml") || lower.endsWith(".yml") || lower.endsWith(".json")) {
        return "yaml";
    }
    const start = text.trimStart();
    // YAML lists start with "-", JSON with "[" or "{", and YAML maps with "monitors:"
    return /^(-|\[|\{|monitors\s*:)/.test(start) ? "yaml" : "csv";
}

/**
 * Split CSV into rows of cells: quoted cells may hold commas, line breaks and "" for a quote
 * @param text The CSV
 * @returns Rows
 */
export function parseCSV(text: string): string[][] {
    const rows: string[][] = [];
    let row: string[] = [];
    let cell = "";
    let quoted = false;
    let i = 0;
    // A byte order mark from spreadsheets
    if (text.charCodeAt(0) === 0xfeff) {
        i = 1;
    }
    for (; i < text.length; i++) {
        const ch = text[i];
        if (quoted) {
            if (ch === '"' && text[i + 1] === '"') {
                cell += '"';
                i++;
            } else if (ch === '"') {
                quoted = false;
            } else {
                cell += ch;
            }
        } else if (ch === '"' && cell === "") {
            quoted = true;
        } else if (ch === ",") {
            row.push(cell);
            cell = "";
        } else if (ch === "\n" || ch === "\r") {
            if (ch === "\r" && text[i + 1] === "\n") {
                i++;
            }
            row.push(cell);
            rows.push(row);
            row = [];
            cell = "";
        } else {
            cell += ch;
        }
    }
    if (cell !== "" || row.length > 0) {
        row.push(cell);
        rows.push(row);
    }
    // Blank lines
    return rows.filter((r) => r.some((c) => c.trim() !== ""));
}

/**
 * A CSV cell as the field's type
 * @param field The field's name
 * @param text The cell
 * @returns The value
 * @throws {Error} When the cell isn't of the field's type
 */
export function cellValue(field: string, text: string): unknown {
    const value = text.trim();
    if (NUMBER_FIELDS.has(field)) {
        const n = Number(value);
        if (!Number.isFinite(n)) {
            throw new Error(`${field} must be a number, not "${value}"`);
        }
        return n;
    }
    if (BOOLEAN_FIELDS.has(field)) {
        const lower = value.toLowerCase();
        if (["true", "yes", "1"].includes(lower)) {
            return true;
        }
        if (["false", "no", "0"].includes(lower)) {
            return false;
        }
        throw new Error(`${field} must be true or false, not "${value}"`);
    }
    if (LIST_FIELDS.has(field)) {
        return value
            .split(";")
            .map((v) => v.trim())
            .filter(Boolean);
    }
    if (JSON_FIELDS.has(field)) {
        try {
            return JSON.parse(value);
        } catch {
            throw new Error(`${field} must be JSON`);
        }
    }
    return value;
}

/**
 * Monitors from CSV with a header row
 * @param text The CSV
 * @returns The monitors, and the problems found
 */
export function csvToMonitors(text: string): ParsedImport {
    const rows = parseCSV(text);
    const errors: string[] = [];
    if (rows.length < 2) {
        return { monitors: [], errors: ["The file needs a header row of field names and a row per monitor."] };
    }
    const header = rows[0].map((h) => h.trim());
    const seen = new Set<string>();
    for (const h of header) {
        if (h === "") {
            errors.push("A column has no name in the header row.");
        } else if (seen.has(h)) {
            errors.push(`The column "${h}" is there twice.`);
        }
        seen.add(h);
    }
    if (errors.length) {
        return { monitors: [], errors };
    }
    const monitors = rows.slice(1).map((cells, i) => {
        const m: Record<string, unknown> = {};
        if (cells.length > header.length) {
            errors.push(`Row ${i + 1}: more cells than the header has names.`);
        }
        header.forEach((field, c) => {
            const cell = cells[c] ?? "";
            if (cell.trim() === "") {
                return;
            }
            try {
                m[field] = cellValue(field, cell);
            } catch (e) {
                errors.push(`Row ${i + 1}: ${(e as Error).message}.`);
            }
        });
        return m;
    });
    return { monitors, errors };
}

/**
 * Monitors from YAML or JSON: a list of monitors, or a map with a "monitors" list
 * @param text The YAML
 * @param load A YAML parser, such as js-yaml's load
 * @returns The monitors, and the problems found
 */
export function yamlToMonitors(text: string, load: (text: string) => unknown): ParsedImport {
    let doc: unknown;
    try {
        doc = load(text);
    } catch (e) {
        return { monitors: [], errors: [`The file isn't valid YAML: ${(e as Error).message.split("\n")[0]}`] };
    }
    const list =
        doc && typeof doc === "object" && !Array.isArray(doc) && "monitors" in doc
            ? (doc as { monitors: unknown }).monitors
            : doc;
    if (!Array.isArray(list)) {
        return { monitors: [], errors: ['The file must be a list of monitors, or have a "monitors" list.'] };
    }
    const errors: string[] = [];
    const monitors = list.map((m, i) => {
        if (!m || typeof m !== "object" || Array.isArray(m)) {
            errors.push(`Row ${i + 1}: a monitor must be a map of fields.`);
            return {};
        }
        return m as Record<string, unknown>;
    });
    return { monitors, errors };
}
