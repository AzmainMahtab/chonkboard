import sys, itertools
def lin(c):
    c=c/255
    return c/12.92 if c<=0.04045 else ((c+0.055)/1.055)**2.4
def lum(h):
    h=h.lstrip('#')
    r,g,b=(int(h[i:i+2],16) for i in (0,2,4))
    return 0.2126*lin(r)+0.7152*lin(g)+0.0722*lin(b)
def ratio(a,b):
    la,lb=lum(a),lum(b)
    hi,lo=max(la,lb),min(la,lb)
    return (hi+0.05)/(lo+0.05)
pairs=[l.split() for l in sys.stdin if l.strip() and not l.startswith('#')]
for name,fg,bg,need in pairs:
    r=ratio(fg,bg)
    ok='PASS' if r>=float(need) else 'FAIL'
    print(f"{name:34} {fg} on {bg}  {r:5.2f}:1  need {need}  {ok}")
