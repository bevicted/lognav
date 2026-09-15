# Exit codes

`lognav` uses these process exit codes:

| Code | Meaning                                                                                                                                  |
| ---- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `0`  | Command completed successfully.                                                                                                          |
| `1`  | General failure, including mixed headless-query results.                                                                                 |
| `2`  | Command-line usage error; stderr prints one sanitized error followed by the failed command's full usage.                                 |
| `65` | Every selected headless-query target rejected the Dataprime query.                                                                       |
| `66` | Required input was not found, such as a named snapshot.                                                                                  |
| `69` | A required ICL or IAM service was unavailable, including noninteractive authentication required by every selected headless-query target. |
| `78` | A command that needs configuration could not load or validate it.                                                                        |

`SIGINT` exits with `130`; `SIGTERM` exits with `143`. These signal exits are
outside the BSD `sysexits(3)` categories used by the nonzero codes above.

Usage errors leave stdout empty. Use `lognav <command> --help` for detailed command behavior.
