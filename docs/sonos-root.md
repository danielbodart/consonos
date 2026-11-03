# The Sonos device root key

Every Sonos speaker serves its local API over TLS with a certificate of its
own, issued to the speaker's MAC address and signed by **Sonos Device
Authentication Root CA**. A speaker sends only that one certificate. The root
is a trust anchor the Sonos apps carry built in; Sonos does not publish it.

To verify a signature you need the signer's public key, not a hash of it, so
consonos needs the root's key. `sonos-root.pem` is that key. It was recovered
from the speakers' own signatures.

## How

The certificates are signed with RSA, PKCS#1 v1.5 and SHA-256. For a
signature `s` over a certificate body, with the root's modulus `n` and
exponent `e` (65537):

    s^e ≡ pad(SHA256(body))  (mod n)

so `s^e - pad(SHA256(body))` is a multiple of `n`. Every certificate the root
signed gives another multiple, and the greatest common divisor of a few of
them is `n` itself. Small cofactors that survive are stripped by trial
division.

[`scripts/recover-root.py`](../scripts/recover-root.py) does that. Each `s^e`
is a number of about 268 million bits (a 4096-bit signature to the power
65537), so it uses GMP through gmpy2; the first gcd takes about a minute.

```sh
for ip in <speaker addresses>; do
  openssl s_client -connect $ip:1443 </dev/null 2>/dev/null | openssl x509 > $ip.pem
done
python3 scripts/recover-root.py *.pem
```

It prints the modulus and whether it verifies every signature it was built
from.

## Provenance

The key in this repository was recovered on 2026-11-03 from four speakers: a
Ray, a One and two Play:1s, whose certificates are in [`testdata`](../testdata).
It is a 4096-bit modulus, it verifies all four signatures, and
`TestRealCertificatesVerify` checks that it still does on every build.

SubjectPublicKeyInfo SHA-256:
`f3c972f64d45808e0931da21cf9f9562825b13a0784f0955963533820a54b6c6`.

If Sonos rotates the root, speakers with new certificates will be refused.
Recover the new key the same way and pass it with `-sonos-root`.
