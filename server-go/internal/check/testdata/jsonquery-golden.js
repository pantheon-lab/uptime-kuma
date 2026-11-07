// Usage: node jsonquery-golden.js <node_modules> <out.json>
// Runs the Node.js backend's evaluateJsonQuery (src/util.ts at 921745bf^) with jsonata 2.2.2.
const path = require("path");
const jsonata = require(path.join(process.argv[2], "jsonata"));

/**
 * The Node.js backend's evaluateJsonQuery, copied unchanged apart from types
 * @param {string} data Response body
 * @param {string} jsonPath JSONata expression
 * @param {string} jsonPathOperator Comparison
 * @param {string} expectedValue Expected value
 * @returns {Promise<{status: boolean, response: any}>} Result and the queried value
 */
async function evaluateJsonQuery(data, jsonPath, jsonPathOperator, expectedValue) {
    let response;
    try {
        response = JSON.parse(data);
    } catch {
        response = typeof data === "object" || typeof data === "number" ? data : data.toString();
    }
    try {
        response = jsonPath ? await jsonata(jsonPath).evaluate(response) : response;
        if (response === null || response === undefined) {
            throw new Error("Empty or undefined response. Check query syntax and response structure");
        }
        if (Array.isArray(response)) {
            const responseStr = JSON.stringify(response);
            const truncatedResponse = responseStr.length > 25 ? responseStr.substring(0, 25) + "...]" : responseStr;
            throw new Error(
                "JSON query returned the array " +
                    truncatedResponse +
                    ", but a primitive value is required. " +
                    "Modify your query to return a single value via [0] to get the first element or use an aggregation like $count(), $sum() or $boolean()."
            );
        }
        if (typeof response === "object" || response instanceof Date || typeof response === "function") {
            throw new Error(
                `The post-JSON query evaluated response from the server is of type ${typeof response}, which cannot be directly compared to the expected value`
            );
        }
        let jsonQueryExpression;
        switch (jsonPathOperator) {
            case ">":
            case ">=":
            case "<":
            case "<=":
                jsonQueryExpression = `$number($.value) ${jsonPathOperator} $number($.expected)`;
                break;
            case "!=":
                jsonQueryExpression = "$.value != $.expected";
                break;
            case "==":
                jsonQueryExpression = "$.value = $.expected";
                break;
            case "contains":
                jsonQueryExpression = "$contains($.value, $.expected)";
                break;
            default:
                throw new Error(`Invalid condition ${jsonPathOperator}`);
        }
        const status = await jsonata(jsonQueryExpression).evaluate({
            value: response.toString(),
            expected: expectedValue.toString(),
        });
        if (status === undefined) {
            throw new Error(
                "Query evaluation returned undefined. Check query syntax and the structure of the response data"
            );
        }
        return { status, response };
    } catch (err) {
        response = JSON.stringify(response);
        response = response && response.length > 50 ? `${response.substring(0, 100)}… (truncated)` : response;
        throw new Error(`Error evaluating JSON query: ${err.message}. Response from server was: ${response}`);
    }
}

const body = JSON.stringify({
    status: "ok",
    count: 42,
    ratio: 0.25,
    big: 1e21,
    tiny: 0.0000005,
    up: true,
    name: "Ünïcödé service",
    items: [
        { id: 1, state: "up", load: 0.5 },
        { id: 2, state: "down", load: 1.5 },
        { id: 3, state: "up", load: 2 },
    ],
    nested: { a: { b: { c: "deep" } } },
    empty: null,
    version: "1.10.2",
    text: "x".repeat(120),
});
const cases = [
    ["status", "==", "ok"],
    ["status", "!=", "ok"],
    ["status", "contains", "o"],
    ["count", ">", "41"],
    ["count", ">=", "42"],
    ["count", "<", "42"],
    ["count", "<=", "42"],
    ["count", "==", "42"],
    ["ratio", "==", "0.25"],
    ["big", "==", "1e+21"],
    ["tiny", "==", "5e-7"],
    ["up", "==", "true"],
    ["name", "contains", "cödé"],
    ["$count(items)", "==", "3"],
    ["$count(items[state='up'])", ">=", "2"],
    ["items[0].state", "==", "up"],
    ["items[id=2].load", ">", "1"],
    ["$sum(items.load)", "==", "4"],
    ["nested.a.b.c", "==", "deep"],
    ["missing", "==", "x"],
    ["empty", "==", "x"],
    ["items", "==", "x"],
    ["nested", "==", "x"],
    ["items.state", "==", "up"],
    ["status", ">", "1"],
    ["count", ">", "abc"],
    ["$string(count)", "==", "42"],
    ["$uppercase(status)", "==", "OK"],
    ["$boolean(items)", "==", "true"],
    ["version", "contains", "1.10"],
    ["text", "==", "x"],
    ["", "contains", "status"],
    ["count * 2", "==", "84"],
    ["$exists(missing)", "==", "false"],
    ["items[load>1].id", "==", "x"],
    ["status = 'ok'", "==", "true"],
];
(async () => {
    const out = [];
    for (const [jsonPath, op, expected] of cases) {
        let msg;
        let ok;
        try {
            const { status, response } = await evaluateJsonQuery(body, jsonPath, op, expected);
            ok = !!status;
            msg = status
                ? `JSON query passes (comparing ${response} ${op} ${expected})`
                : `JSON query does not pass (comparing ${response} ${op} ${expected})`;
        } catch (e) {
            ok = false;
            msg = e.message;
        }
        out.push({ jsonPath, op, expected, ok, msg });
    }
    out.push({
        body: "plain text",
        jsonPath: "",
        op: "contains",
        expected: "plain",
        ...(await evaluateJsonQuery("plain text", "", "contains", "plain").then((r) => ({
            ok: !!r.status,
            msg: `JSON query passes (comparing ${r.response} contains plain)`,
        }))),
    });
    require("fs").writeFileSync(process.argv[3], JSON.stringify({ body, cases: out }, null, 1));
    console.log(out.length, "cases");
})();
