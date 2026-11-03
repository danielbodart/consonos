# Recover an RSA modulus from several PKCS#1 v1.5 SHA-256 signatures made by
# the same key: s^e - pad(H(m)) is a multiple of n for every signature, so the
# gcd of a few of them is n (times, at most, small factors to strip).
import hashlib, sys, time
import gmpy2
from cryptography import x509

DIGESTINFO_SHA256 = bytes.fromhex("3031300d060960864801650304020105000420")

def padded(tbs, k):
    t = DIGESTINFO_SHA256 + hashlib.sha256(tbs).digest()
    return int.from_bytes(b"\x00\x01" + b"\xff" * (k - len(t) - 3) + b"\x00" + t, "big")

certs = [x509.load_pem_x509_certificate(open(p, "rb").read()) for p in sys.argv[1:]]
e = 65537
k = len(certs[0].signature)
print("modulus bytes", k, flush=True)

g = None
for c in certs:
    t = time.time()
    s = gmpy2.mpz(int.from_bytes(c.signature, "big"))
    v = s**e - padded(c.tbs_certificate_bytes, k)
    g = v if g is None else gmpy2.gcd(g, v)
    print("cert", c.subject.rfc4514_string()[:16], "bits of gcd", g.bit_length(), "%.1fs" % (time.time() - t), flush=True)

for p in range(2, 1 << 20):
    while g % p == 0 and g.bit_length() > 8 * k:
        g //= p
n = int(g)
print("n bits", n.bit_length())
ok = all(pow(int.from_bytes(c.signature, "big"), e, n) == padded(c.tbs_certificate_bytes, k) for c in certs)
print("verifies all:", ok)
print("n hex", hex(n))
