import sys, collections
# Resume um log do wrapper: número de comandos, bytes totais, por comando.
text = open(sys.argv[1]).read()
# O wrapper repete "### CMD: " antes de cada argumento (printf reaproveita o
# formato), então um bloco começa numa linha que contém o prefixo e termina
# em "### STATUS".
blocks = [b for b in text.split('\n### STATUS: ') ]
per = collections.Counter(); bytes_per = collections.Counter(); total = 0; errs = 0
entries = []
for i in range(len(blocks) - 1):
    body = blocks[i]
    status_part = blocks[i + 1].split('\n', 1)[0]
    start = body.rfind('### CMD: ')
    # a linha de comando é a primeira linha a partir do primeiro "### CMD: " do bloco
    first = body.find('### CMD: ')
    cmd_line = body[first:].split('\n', 1)[0] if first >= 0 else ''
    args = [a.strip() for a in cmd_line.split('### CMD: ') if a.strip()]
    entries.append((' '.join(args), status_part))
for cmdline, status_part in entries:
    status, _, b = status_part.partition(' BYTES: ')
    b = int(b.strip().split()[0] or 0) if b.strip() else 0
    rest = ''

    parts = cmdline.split()
    key = parts[0].split('/')[-1]
    if key == 'codegraph':
        sub, skip = '?', False
        for p in parts[1:]:
            if skip: skip = False; continue
            if p == '--repo': skip = True; continue
            if p.startswith('--'): continue
            sub = p; break
        key = 'codegraph ' + sub
    per[key] += 1; bytes_per[key] += b; total += b
    if status.strip() != '0': errs += 1
print(f"commands: {sum(per.values())}  total output bytes: {total}  (~{total//4} tokens)  non-zero exits: {errs}")
for k, n in per.most_common():
    print(f"  {k:24s} calls={n:3d} bytes={bytes_per[k]:7d}")
