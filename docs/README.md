# docs

| File | What it is |
|---|---|
| [`00-foundation.md`](00-foundation.md) | The 11-section design doc, and the source of truth for this project. Migrated from the CUBRID Systems roadmap on graduation. |
| [`01-00-survey_overview.md`](01-00-survey_overview.md) | Survey series index — the five decisions every provisioner makes, the comparison matrix, and the derived implications |
| [`01-01-survey_postgresql.md`](01-01-survey_postgresql.md) · [`01-02-survey_mysql.md`](01-02-survey_mysql.md) · [`01-03-survey_mongodb.md`](01-03-survey_mongodb.md) · [`01-04-survey_tidb.md`](01-04-survey_tidb.md) | One per comparable system, in reading order |
| [`01-05-survey_cubrid-gap-and-measurement.md`](01-05-survey_cubrid-gap-and-measurement.md) | Where CUBRID stands, eleven gaps, and how each is measured |
| [`findings/`](findings/) | What running it actually showed |
| [`assets/`](assets/) | Survey figures (SVG) |

Cross-references inside the survey series run strictly backwards — `01-05` may
cite `01-03`, never the other way — so a new per-system file slots in at the next
free index without editing earlier ones.
