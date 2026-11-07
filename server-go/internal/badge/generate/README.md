# Badge data

Both scripts read the npm packages that the Node.js backend used, installed in a scratch directory:

```bash
mkdir /tmp/badge-npm && cd /tmp/badge-npm && npm init -y
npm install --ignore-scripts badge-maker@3.3.1 chroma-js@2.4.2
```

- `generate.py` writes the character width tables in `../data` from anafanafo, and `../css_colors.go` from
  color-name: `python3 -I generate.py /tmp/badge-npm/node_modules`
- `golden.js` writes `../testdata/golden.json`, the badges and colours the tests compare with:
  `node golden.js /tmp/badge-npm/node_modules ../testdata/golden.json`
