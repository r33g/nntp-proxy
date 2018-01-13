# nntp-proxy
an NNTP proxy that can pool connections

If you see the following error (something wrong with go):

`remote error: tls: bad record MAC`

You'll need to run this

`sudo /sbin/ethtool -K eth0 gso off tso off sg off gro off`
