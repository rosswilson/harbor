import js from "@eslint/js";
import globals from "globals";

export default [
  { ignores: ["node_modules/**", "work/**", ".venv/**"] },
  js.configs.recommended,
  {
    files: ["web/**/*.js"],
    languageOptions: { globals: globals.browser, sourceType: "script" },
    rules: { "no-unused-vars": ["error", { caughtErrors: "none" }] },
  },
];
