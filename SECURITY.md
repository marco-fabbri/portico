# Security policy

Portico is pre-release: there is no supported installation yet and no
release to upgrade. Security matters from the first commit anyway, because
the whole point of the appliance is what it refuses to let through.

## Reporting a vulnerability

Please do not open a public issue for a vulnerability. Use GitHub's
private vulnerability reporting on this repository ("Report a
vulnerability" under the Security tab). Say what you found, how to
reproduce it and which commit you looked at; a fix or an answer follows
within a reasonable time, and the report is credited unless you prefer
otherwise.

## What counts

Anything that breaks one of the guarantees the design makes:

- a request reaching Prism Central through the public path without a valid
  Portico session, or a Prism Central local account authenticating from
  the public path (including with the identity provider down);
- a tenant seeing or acting on another tenant's project;
- a human administrator's credential stored by Portico, or a secret
  readable without the master key;
- a secret, token or session id written to a log.

Findings about the lab scripts are welcome too; they handle real
credentials.
