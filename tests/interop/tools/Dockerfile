# Reference Python tooling (meshtastic CLI/TCPInterface, sniffer, vector capture)
# that runs *inside* the docker mesh network, so multicast works regardless of
# the host firewall.
FROM python:3.13-slim
RUN pip install --no-cache-dir meshtastic==2.7.11 cryptography==50.0.1 protobuf==7.36.1
WORKDIR /interop
ENTRYPOINT ["python"]
