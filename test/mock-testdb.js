const { sync: rimrafSync } = require("rimraf");
const Database = require("../server/database");
const { Settings } = require("../server/settings");
class TestDB {
    dataDir;

    constructor(dir = "./data/test") {
        this.dataDir = dir;
    }

    async create() {
        Database.initDataDir({ "data-dir": this.dataDir });
        // Set TEST_DB_POSTGRES_PASSWORD (and optionally _HOST, _PORT) to run on TimescaleDB,
        // in a throwaway database per test file
        Database.dbConfig = process.env.TEST_DB_POSTGRES_PASSWORD
            ? {
                  type: "postgres",
                  hostname: process.env.TEST_DB_POSTGRES_HOST || "127.0.0.1",
                  port: process.env.TEST_DB_POSTGRES_PORT || "5433",
                  username: "postgres",
                  password: process.env.TEST_DB_POSTGRES_PASSWORD,
                  dbName: `kuma_test_${process.pid}_${Date.now()}`,
              }
            : {
                  type: "sqlite",
              };
        Database.writeDBConfig(Database.dbConfig);
        await Database.connect(true);
        await Database.patch();
    }

    async destroy() {
        const postgresConfig = Database.dbConfig.type === "postgres" ? Database.dbConfig : null;
        await Database.close();
        if (postgresConfig) {
            const { Client } = require("pg");
            const admin = new Client({ ...Database.postgresConnectionOptions(postgresConfig), database: "postgres" });
            await admin.connect();
            await admin.query(`DROP DATABASE IF EXISTS "${postgresConfig.dbName}" WITH (FORCE)`);
            await admin.end();
        }
        Settings.stopCacheCleaner();
        if (this.dataDir) {
            try {
                rimrafSync(this.dataDir);
            } catch (e) {
                console.error("Windows may hold file lock?");
                console.error(e);
            }
        }
    }
}

module.exports = TestDB;
