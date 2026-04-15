# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in atproto-push-gateway, please report it responsibly.

**Email:** jans@dracoblue.de

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact

**Do NOT open public GitHub issues for security vulnerabilities.**

You will receive a response within 48 hours. We will work with you to understand the issue and coordinate a fix before any public disclosure.

## Scope

The following are in scope:
- JWT verification bypass
- Unauthorized push token registration or delivery
- Block suppression bypass (notifications delivered despite blocks)
- Information disclosure (DIDs, tokens, or user data)
- Denial of service via Jetstream or XRPC endpoints

## Supported Versions

Only the latest release is supported with security updates.
