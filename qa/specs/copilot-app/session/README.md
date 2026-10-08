# copilot-app/session

What every Copilot app span may carry: the attribute surface, and what `omit_io` keeps out of it.

| Invariant | Spec | Status |
| --- | --- | --- |
| With `omit_io` on, no prompt, response, tool argument or tool result leaves the machine | [omit-io-keeps-content-out](omit-io-keeps-content-out.md) | draft — passed 19/19 on 2026-10-06 |
| Every attribute on a copilot-app span is in the `DEVELOPMENT.md` contract | [copilot-app-span-carries-no-undeclared-attribute](copilot-app-span-carries-no-undeclared-attribute.md) | draft — runs over the other specs' runs; passed over 8 runs on 2026-10-06 |

Dash0 masks message content on read, so absence of content is proved in the plugin's debug log and
Dash0 is only a second reading. See `## Observe` in [../../../setup.md](../../../setup.md).
