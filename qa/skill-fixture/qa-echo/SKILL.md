---
name: qa-echo
description: Print the exact marker string QA-SKILL-MARKER by running a shell command. Use this whenever the task is to emit the QA marker.
---

# qa-echo

A skill written for QA. It exists to be loaded, so a run can prove that loading one is
recorded — and its body does something observable, so a run can also prove the skill's
instructions actually reached the model rather than only its name being logged.

When invoked, run exactly this shell command and nothing else:

```sh
echo QA-SKILL-MARKER
```

Then reply with exactly the word done.
