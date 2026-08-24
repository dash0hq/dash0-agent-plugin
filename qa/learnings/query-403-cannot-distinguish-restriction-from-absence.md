# A 403 does not tell you whether the dataset exists or the token is restricted

Two different mistakes produce the same response. Querying a dataset the token has no
access to, and querying a dataset name that cannot exist at all, both return
`403 access denied; check your permissions ... access to dataset '<name>' is not
permitted`. Probing with an obviously impossible dataset name confirmed the messages are
identical.

A wrong token is distinguishable: it returns
`401 authentication failed; check your auth token ... The provided auth token is not
known`.

**Why it matters:** the standard preflight for a shared environment is to prove a token
is not dataset-restricted by reading two datasets. That probe is inconclusive here, so a
check written around it would assert something it cannot see.

**How to apply:** check only that the one configured dataset reads and returns rows, and
say plainly that this does not prove the token is unrestricted. On a 403, suspect the
dataset name first, because a typo is far more likely than a scoped token. This project
only ever reads one dataset, so nothing more is needed.
