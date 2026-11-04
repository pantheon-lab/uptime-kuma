const { Bean } = require("redbean-node/dist/bean");
const { BeanModel } = require("redbean-node/dist/bean-model");
const { isEmptyObject } = require("redbean-node/dist/helper/helper");

/**
 * Parsers for the main connection pool, matching what the application gets from
 * MariaDB and SQLite: datetimes stay strings, and counts and sums are numbers.
 * @type {Record<number, (value: string) => any>}
 */
const typeParsers = {
    20: Number, // int8, returned by COUNT and SUM of integers
    1700: Number, // numeric, returned by SUM of bigints
    1082: (value) => value, // date
    1114: (value) => value, // timestamp
    1184: (value) => value, // timestamptz
};

/**
 * pg connection option that applies typeParsers
 * @returns {{getTypeParser: Function}} Value for the "types" option
 */
function pgTypes() {
    const { types } = require("pg");
    return {
        getTypeParser(oid, format) {
            if (format !== "binary" && typeParsers[oid]) {
                return typeParsers[oid];
            }
            return types.getTypeParser(oid, format);
        },
    };
}

/**
 * Patch the knex PostgreSQL client. It is a prototype patch for the same reason as the
 * mysql2 one in database.js: extending the client didn't take effect.
 * @returns {void}
 */
function patchKnexPostgres() {
    const { getDialectByNameOrAlias } = require("knex/lib/dialects");
    const KumaColumnCompilerPG = require("./knex/lib/dialects/postgres/schema/pg-columncompiler");
    const pg = getDialectByNameOrAlias("pg");

    pg.prototype.columnCompiler = function () {
        return new KumaColumnCompilerPG(this, ...arguments);
    };

    // Booleans are stored as smallint, see KumaColumnCompilerPG. better-auth columns
    // still accept this through knex, because pg sends parameters as untyped text.
    // The SQL throughout the application quotes identifiers with backticks, which
    // MariaDB and SQLite accept. Translate them, leaving string literals untouched.
    const positionBindings = pg.prototype.positionBindings;
    pg.prototype.positionBindings = function (sql) {
        return positionBindings.call(
            this,
            sql.replace(/'(?:[^']|'')*'|`/g, (match) => (match === "`" ? '"' : match))
        );
    };

    pg.prototype.prepBindings = function (bindings) {
        return bindings?.map((value) => (typeof value === "boolean" ? Number(value) : value));
    };
}

/**
 * redbean-node doesn't support PostgreSQL. Add the three things it needs on this
 * instance: raw results are in .rows, inserts need RETURNING to get the id, and
 * "LIMIT ?" can't be built through toNative(), because that yields "$1".
 * @param {object} R The redbean-node instance
 * @returns {void}
 */
function patchRedbeanPostgres(R) {
    R.normalizeRaw = async function (sql, data) {
        const result = await this.normalizeRawCore(sql, data);
        this.queryLog(sql);
        return result.rows;
    };

    R.getRow = async function (sql, data = [], autoLimit = false) {
        const rows = await this.normalizeRaw(autoLimit ? `${sql} LIMIT 1` : sql, data);
        return rows.length > 0 ? rows[0] : null;
    };

    // Same as redbean-node's storeCore, apart from .returning("id") on the insert
    R.storeCore = async function (bean, changedFieldsOnly = true) {
        this.devLog("Store", bean.beanMeta.type, bean.id);
        await bean.storeTypeBeanList();
        if (bean instanceof BeanModel) {
            bean.onUpdate();
        }
        if (!this._freeze) {
            await this.updateTableSchema(bean);
        }
        const obj = bean.export(false);
        delete obj.id;
        if (bean.id) {
            if (changedFieldsOnly) {
                for (const key in obj) {
                    if (!(Bean.internalName(key) in bean.beanMeta.old)) {
                        delete obj[key];
                    }
                }
            }
            if (!isEmptyObject(obj)) {
                const query = this.knex(bean.getType()).where({ id: bean.id }).update(obj);
                this.queryLog(query);
                await query;
            }
        } else {
            const query = this.knex(bean.getType()).insert(obj).returning("id");
            this.queryLog(query);
            const result = await query;
            bean.id = result[0].id;
        }
        await bean.storeSharedList();
        await bean.storeOwnList();
        bean.beanMeta.old = {};
        if (bean instanceof BeanModel) {
            bean.onAfterUpdate();
        }
        return bean.id;
    };
}

module.exports = {
    pgTypes,
    patchKnexPostgres,
    patchRedbeanPostgres,
};
