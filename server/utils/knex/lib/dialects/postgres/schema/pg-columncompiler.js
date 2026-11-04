const ColumnCompilerPG = require("knex/lib/dialects/postgres/schema/pg-columncompiler");

/**
 * The application was written for SQLite and MariaDB: booleans are 0/1 and datetimes
 * are timezone-less strings, in SQL as well as in JavaScript. Keep those types instead
 * of rewriting every query and comparison.
 *
 * better-auth talks to PostgreSQL through its own driver and expects native types, so
 * its tables keep real booleans and timestamptz.
 */
class KumaColumnCompilerPG extends ColumnCompilerPG {
    /**
     * @returns {boolean} Whether this column belongs to a better-auth table
     */
    isBetterAuthTable() {
        return String(this.tableCompiler.tableNameRaw).startsWith("better_auth");
    }

    /**
     * @returns {string} Column type of a boolean
     */
    bool() {
        return this.isBetterAuthTable() ? "boolean" : "smallint";
    }

    /**
     * @returns {string} Column type of a datetime
     */
    datetime() {
        return this.isBetterAuthTable() ? "timestamptz" : "timestamp(3)";
    }

    /**
     * @returns {string} Column type of a timestamp
     */
    timestamp() {
        return this.datetime();
    }
}

module.exports = KumaColumnCompilerPG;
