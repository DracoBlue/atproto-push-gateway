# Changelog

## [1.2.0](https://github.com/DracoBlue/atproto-push-gateway/compare/v1.1.0...v1.2.0) (2026-04-23)


### Features

* **xrpc:** backfill historical blocks on first token registration ([bcca48b](https://github.com/DracoBlue/atproto-push-gateway/commit/bcca48b6f7b8723fe78c9d549fdef90dd3f76af6))


### Bug Fixes

* cap token/appId length and tokens-per-DID ([e5a5b18](https://github.com/DracoBlue/atproto-push-gateway/commit/e5a5b18380d9d6ebaa957e2ec551964b9b55378f))
* cap XRPC body size and DID document size ([542c248](https://github.com/DracoBlue/atproto-push-gateway/commit/542c24815145e21f0c9317e16885a0909a8ca0c4))
* **did:** block SSRF via did:web to private/loopback/IMDS addresses ([72c7a4d](https://github.com/DracoBlue/atproto-push-gateway/commit/72c7a4dc4fcb10ca35cfc9a48cc865d0cca78d0d))
* **did:** cap DNS lookup at 5 seconds ([f6a53ef](https://github.com/DracoBlue/atproto-push-gateway/commit/f6a53ef7c3147f7303b4b629a4cc107318ea96ab))
* **did:** require exact length for multibase-encoded keys ([a696715](https://github.com/DracoBlue/atproto-push-gateway/commit/a696715f3b8bc7c5a555b2114bc4e3432d64668d))
* **jetstream:** add websocket ping/pong and read timeouts ([1e6d2ec](https://github.com/DracoBlue/atproto-push-gateway/commit/1e6d2ec4f30cd0ee81dadb2b179a5d95fec83893))
* **jetstream:** move push dispatch to bounded worker pool ([1563a59](https://github.com/DracoBlue/atproto-push-gateway/commit/1563a5987ef2eeaba2faa9d22184dc376ac7e463))
* **push:** add 10s HTTP timeout to Expo and FCM senders ([eb57563](https://github.com/DracoBlue/atproto-push-gateway/commit/eb57563c65e76bc95a5c79cc443f597fc5f323ea))
* **push:** remove stale tokens on APNs 410 / FCM UNREGISTERED ([8cc6f75](https://github.com/DracoBlue/atproto-push-gateway/commit/8cc6f75df01c5826f261bc5fc76c907d50e8c5a6))
* **server:** add HTTP timeouts and header size cap ([9a97ebf](https://github.com/DracoBlue/atproto-push-gateway/commit/9a97ebfb8ec0d9cd94ddd5df49fe6feba5cca782))
* **server:** guardrail DEV_MODE against accidental production use ([d865416](https://github.com/DracoBlue/atproto-push-gateway/commit/d86541676da84c02b11683a998ea5529c45dc86e))
* **server:** validate PUSH_GATEWAY_DID is a did:web at startup ([5163ec5](https://github.com/DracoBlue/atproto-push-gateway/commit/5163ec5ac4e01ab32b6d59218d195ea9df335854))
* **store:** wrap per-DID token cap check in a transaction ([da0864f](https://github.com/DracoBlue/atproto-push-gateway/commit/da0864fd5067d03b7a65725f4fe3ae27197f919e))
* **xrpc:** add lxm validation ([c1839d4](https://github.com/DracoBlue/atproto-push-gateway/commit/c1839d402aadeec36c29ad474da9f64f3ac0686e))
* **xrpc:** add lxm validation ([8d1bf5c](https://github.com/DracoBlue/atproto-push-gateway/commit/8d1bf5c6606870df208554e5aafd36fda36c2ec4))
* **xrpc:** cap JWT exp at 5 minutes in the future ([5e77b2d](https://github.com/DracoBlue/atproto-push-gateway/commit/5e77b2de25033e0b360cdc41d7dbefe93edabf04))
* **xrpc:** reject JWTs on any verification failure ([8b9190a](https://github.com/DracoBlue/atproto-push-gateway/commit/8b9190a47cceba253e709555242e7adcd5d1ac57))
* **xrpc:** require request body serviceDid to match configured DID ([6da5138](https://github.com/DracoBlue/atproto-push-gateway/commit/6da513815eb9f543e5e130c60b5615843e7c9c06))
* **xrpc:** validate JWT aud claim against configured service DID ([c2e777b](https://github.com/DracoBlue/atproto-push-gateway/commit/c2e777bede79291583316edda9548fa7deb2f3cb))
* **xrpc:** verify JWT before parsing request body ([63f7ebb](https://github.com/DracoBlue/atproto-push-gateway/commit/63f7ebb01f192b617cae68e72b6bf4416de1fffe))

## [1.1.0](https://github.com/DracoBlue/atproto-push-gateway/compare/v1.0.0...v1.1.0) (2026-04-15)


### Features

* initial open-source release ([8924882](https://github.com/DracoBlue/atproto-push-gateway/commit/89248825611c34a001249c935be3c0aa5a35416a))
* trigger initial release ([fb6d2c6](https://github.com/DracoBlue/atproto-push-gateway/commit/fb6d2c6d4cef3f6492bb5afdd52fab3a8e892ae4))

## 1.0.0 (2026-04-15)


### Features

* initial open-source release ([8924882](https://github.com/DracoBlue/atproto-push-gateway/commit/89248825611c34a001249c935be3c0aa5a35416a))
* trigger initial release ([fb6d2c6](https://github.com/DracoBlue/atproto-push-gateway/commit/fb6d2c6d4cef3f6492bb5afdd52fab3a8e892ae4))
