# Private-space quota prerequisite (0.7.1)

This small F2 fix precedes Stage 3 of the private-spaces plan. It changes the
shared REST/MCP access boundary, not space storage or human account features.

HTTP ingress still limits all requests before authentication (240 per client
and 2400 globally per minute). Both ingress counters now reserve atomically.
Unknown spaces and invalid keys return unauthorized; a read key attempting a
write returns forbidden. None consumes operation allowance.

After authorization, global, space, and client operation limits reserve under
one lock. Any exhausted limit rejects the operation without charging the others.
The longest exhausted-window wait is retained. Admitted operations that later
fail validation, revision checks, queue admission, or Git work keep their charge.
Public operations use the same atomic reservation without requiring credentials.

No schema, key, configuration, or Apache change is needed. The owner builds,
installs, and restarts the service normally. Limits remain in memory and reset
on restart; clients sharing an IP still share ingress/client limits.

## Focused owner checks

Use a disposable configured private space and separate client IPs where needed.
Keep checks within one rate window, away from its boundary.

1. Send missing/invalid-key write attempts, plus read-key write attempts. Expect
   unauthorized/forbidden, then ingress throttling for excessive traffic. A valid
   writer from another client must retain its full operation allowance.
2. Exhaust one authorized client's operation limit. Repeat rejected attempts,
   then confirm a second client can use the remaining space/global allowance.
3. Repeat through REST and MCP; accepted operations through either transport
   consume the same operation buckets. Unknown-space attempts must not reduce
   another space's operation allowance.
4. With low disposable operation limits, issue concurrent authorized requests.
   Admissions must not exceed any configured ceiling; rejections report a wait.
5. Exhaust one client's ingress limit, continue rejected requests, and confirm
   they do not consume the remaining global ingress allowance for another client.

Source and formatting review only were performed for this patch. Builds and
runtime validation are left to the owner. F3 recent-history work remains separate.
