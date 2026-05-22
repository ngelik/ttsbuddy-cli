# On-call handoff: search latency

Service: public docs search
Status: mitigated
Severity: SEV-2

At 14:05 UTC, p95 search latency rose from 420 ms to 2.8 seconds after the nightly index job started using the same database pool as production queries. Error rate stayed below 0.2 percent, but users saw slow autocomplete and delayed results.

Mitigation: the indexing worker was paused, read replicas recovered by 14:19 UTC, and the pool limit was reduced from 40 to 12 connections. The next engineer should watch p95 latency, database CPU, and queued index jobs for one hour.

Follow-up: split indexing traffic onto the background pool, add an alert for pool saturation above 80 percent, and run the missed index job after traffic drops.
