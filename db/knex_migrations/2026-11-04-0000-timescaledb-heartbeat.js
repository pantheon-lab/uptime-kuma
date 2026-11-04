// On PostgreSQL, make heartbeat a TimescaleDB hypertable partitioned by time. Unique
// indexes on a hypertable have to include the time column, so the primary key changes
// from (id) to (id, time). ids stay unique, because they still come from the sequence.
exports.up = async function (knex) {
    if (knex.client.dialect !== "postgresql") {
        return;
    }
    await knex.raw("ALTER TABLE heartbeat DROP CONSTRAINT heartbeat_pkey");
    await knex.raw("ALTER TABLE heartbeat ADD PRIMARY KEY (id, time)");
    await knex.raw(
        "SELECT create_hypertable('heartbeat', by_range('time', INTERVAL '1 day'), migrate_data => true, create_default_indexes => false)"
    );
};

exports.down = async function () {
    // A hypertable can't be converted back in place.
};
