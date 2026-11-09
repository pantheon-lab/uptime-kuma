import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";
import visualizer from "rollup-plugin-visualizer";
import viteCompression from "vite-plugin-compression";
import postCssScss from "postcss-scss";
import postcssRTLCSS from "postcss-rtlcss";

const viteCompressionFilter = /\.(js|mjs|json|css|html|svg)$/i;

// https://vitejs.dev/config/
export default defineConfig({
    server: {
        port: 3000,
        // The Go backend (server-go), for `npm run dev`. Proxied, so that the page and the backend
        // share an origin: the backend checks the Origin of sign-ins and sends no CORS headers.
        proxy: Object.fromEntries(
            ["/api", "/upload", "/screenshots", "/healthz", "/mcp", "/status/.*/rss"].map((path) => [
                path.includes("*") ? `^${path}` : path,
                { target: process.env.KUMA_BACKEND ?? "http://127.0.0.1:3001", changeOrigin: false },
            ])
        ),
    },
    define: {
        FRONTEND_VERSION: JSON.stringify(process.env.npm_package_version),
        "process.env": {},
    },
    plugins: [
        vue(),
        visualizer({
            filename: "tmp/dist-stats.html",
        }),
        viteCompression({
            algorithm: "gzip",
            filter: viteCompressionFilter,
        }),
        viteCompression({
            algorithm: "brotliCompress",
            filter: viteCompressionFilter,
        }),
    ],
    css: {
        postcss: {
            parser: postCssScss,
            map: false,
            plugins: [postcssRTLCSS],
        },
        preprocessorOptions: {
            scss: {
                loadPaths: [process.cwd()],
                quietDeps: true,

                // Most of them are coming from boostrap unfortunately
                silenceDeprecations: [
                    "import",
                    "global-builtin",
                    "if-function",
                    "color-functions",
                    "abs-percent",
                    "function-units",
                ],
            },
        },
    },
    build: {
        commonjsOptions: {
            include: [/.js$/],
        },
    },
});
