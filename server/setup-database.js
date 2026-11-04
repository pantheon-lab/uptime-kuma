const fs = require("fs");
const path = require("path");
const { log } = require("../src/util");
const Database = require("./database");

const DEFAULT_POSTGRES_PORT = "5432";
const DEFAULT_POSTGRES_DB = "uptime_kuma";

/**
 * Reads a configuration value from an environment variable or a Docker secrets file.
 * If both the direct env var and the _FILE variant are set, an error is thrown.
 * @param {string} envName The base name of the environment variable (e.g., "UPTIME_KUMA_DB_PASSWORD")
 * @returns {string|undefined} The value from the env var, file contents (trimmed), or undefined if neither is set
 * @throws {Error} If both the direct env var and the _FILE variant are set
 */
function getEnvOrFile(envName) {
    const directValue = process.env[envName];
    const fileValue = process.env[envName + "_FILE"];

    if (directValue && fileValue) {
        throw new Error(`Both ${envName} and ${envName}_FILE are set. Please use only one.`);
    }

    if (fileValue) {
        try {
            return fs.readFileSync(fileValue, "utf8").trim();
        } catch (err) {
            throw new Error(`Failed to read ${envName}_FILE at ${fileValue}: ${err.message}`);
        }
    }

    return directValue;
}

/**
 * Decide which database to use and write db-config.json. TimescaleDB is the default.
 *
 * Priority: UPTIME_KUMA_DB_TYPE > existing db-config.json > TimescaleDB from the
 * UPTIME_KUMA_DB_* variables. An existing configuration keeps working, so an upgrade
 * can't silently start on an empty database.
 * @returns {void}
 * @throws {Error} If nothing usable is configured
 */
function configureDatabase() {
    const type = process.env.UPTIME_KUMA_DB_TYPE;
    let existing = null;
    try {
        existing = Database.readDBConfig();
    } catch (e) {
        log.debug("setup-database", "db-config.json is not found or invalid: " + e.message);
    }

    if (!type && existing) {
        log.debug("setup-database", "Using db-config.json");
        return;
    }

    if (!type && fs.existsSync(path.join(Database.dataDir, "kuma.db"))) {
        throw new Error(
            "Found a SQLite database (kuma.db) but no db-config.json. Set UPTIME_KUMA_DB_TYPE=sqlite to keep using it."
        );
    }

    const dbConfig = existing ?? {};
    dbConfig.type = type || "postgres";
    dbConfig.hostname = process.env.UPTIME_KUMA_DB_HOSTNAME;
    dbConfig.port = process.env.UPTIME_KUMA_DB_PORT;
    dbConfig.dbName = process.env.UPTIME_KUMA_DB_NAME;
    dbConfig.username = getEnvOrFile("UPTIME_KUMA_DB_USERNAME");
    dbConfig.password = getEnvOrFile("UPTIME_KUMA_DB_PASSWORD");
    dbConfig.socketPath = process.env.UPTIME_KUMA_DB_SOCKET?.trim();
    dbConfig.ssl = getEnvOrFile("UPTIME_KUMA_DB_SSL")?.toLowerCase() === "true";
    dbConfig.ca = getEnvOrFile("UPTIME_KUMA_DB_CA");

    if (dbConfig.type === "postgres") {
        dbConfig.port ||= DEFAULT_POSTGRES_PORT;
        dbConfig.dbName ||= DEFAULT_POSTGRES_DB;
        dbConfig.username ||= "postgres";
        if (!dbConfig.hostname || !dbConfig.password) {
            throw new Error(
                "TimescaleDB is not configured. Set UPTIME_KUMA_DB_HOSTNAME and UPTIME_KUMA_DB_PASSWORD, and optionally UPTIME_KUMA_DB_PORT, UPTIME_KUMA_DB_NAME and UPTIME_KUMA_DB_USERNAME."
            );
        }
    }

    log.info("setup-database", `Using database type ${dbConfig.type} from the environment`);
    Database.writeDBConfig(dbConfig);
}

module.exports = {
    configureDatabase,
};
