# opencode-v2/session

One OpenCode turn, end to end.

| Spec | Asserts |
| --- | --- |
| [single-turn-agrees-with-its-session-export](single-turn-agrees-with-its-session-export.md) | One `chat` and one `execute_tool` per tool part; tokens equal the export's |
| [default-privacy-redacts-but-keeps-messages](default-privacy-redacts-but-keeps-messages.md) | With `omit_io` at its default, messages are present and `<REDACTED>`, and no content leaks |
| [io-is-exported-when-omit-io-is-off](io-is-exported-when-omit-io-is-off.md) | With `omit_io: false`, the prompt, reply and tool IO reach the spans |
| [tool-failure-sets-the-span-status](tool-failure-sets-the-span-status.md) | A failed tool is an ERROR span; the turn is not |
| [opencode-v2-span-carries-no-undeclared-attribute](opencode-v2-span-carries-no-undeclared-attribute.md) | Every attribute is in `DEVELOPMENT.md` |

Not written: MCP and skills. An MCP call keeps OpenCode's flattened `<server>_<tool>` name and no MCP
server attribute, since the plugin API cannot tell it from a local tool in the same namespace;
`qa-compare.py` fails any span that claims one. No fixture registers an MCP server or a skill for an
OpenCode run yet.
