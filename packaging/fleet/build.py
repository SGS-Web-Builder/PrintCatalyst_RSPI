"""Build the standalone metadata-only fleet service for Linux."""
import argparse, hashlib, io, json, os, subprocess, tarfile, tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--arch',choices=['amd64','arm64'],default='amd64')
    a=p.parse_args()
    with tempfile.TemporaryDirectory(prefix='fleet-build-') as tmp:
        binary=Path(tmp)/'fleet-server'
        subprocess.run(['go','build','-trimpath','-o',str(binary),'./cmd/fleet-server'],cwd=ROOT/'runtime',env=dict(os.environ,GOOS='linux',GOARCH=a.arch,CGO_ENABLED='0'),check=True)
        payload={'fleet-server':binary.read_bytes()}
        for name in ['README.md','config.example.json','printcatalyst-fleet.service']:
            payload[name]=(ROOT/'packaging/fleet'/name).read_bytes().replace(b'\r\n',b'\n')
        manifest={'platform':'linux/'+a.arch,'sha256':{k:hashlib.sha256(v).hexdigest() for k,v in payload.items()}}
        payload['manifest.json']=json.dumps(manifest,indent=2).encode()
        out=ROOT/'build'/('printcatalyst-fleet-'+a.arch+'.tar.gz')
        with tarfile.open(out,'w:gz') as archive:
            for name,data in payload.items():
                item=tarfile.TarInfo('printcatalyst-fleet/'+name);item.size=len(data);item.mode=0o755 if name=='fleet-server' else 0o644
                archive.addfile(item,io.BytesIO(data))
        print(out)
if __name__=='__main__':main()
