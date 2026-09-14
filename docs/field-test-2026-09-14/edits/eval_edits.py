import subprocess, sys, os, re
SP = sys.argv[1]
def diff(d):
    return subprocess.run(["git","-C",d,"diff"],capture_output=True,text=True).stdout
def files(d):
    out = subprocess.run(["git","-C",d,"diff","--name-only"],capture_output=True,text=True).stdout.split()
    return [f for f in out if not f.startswith(".codegraph")]
def has(d, path, pattern):
    p = os.path.join(d, path)
    try: return re.search(pattern, open(p, encoding="utf-8").read()) is not None
    except FileNotFoundError: return False
O = "src/main/java/org/springframework/samples/petclinic/owner/"
checks = {
 "e1": {
   "required": [
     (O+"Owner.java", r"public void scheduleVisit\(Integer petId, Visit visit\)"),
     (O+"VisitController.java", r"owner\.scheduleVisit\(petId, visit\)"),
     ("src/test/java/org/springframework/samples/petclinic/service/ClinicServiceTests.java", r"owner6\.scheduleVisit\("),
   ],
   "forbidden_changed": [
     (O+"Pet.java", r"public void addVisit\(Visit visit\)"),
     (O+"VisitController.java", r"pet\.addVisit\(visit\)"),
     (O+"Owner.java", r"pet\.addVisit\(visit\)"),
   ],
   "max_files": 3,
 },
 "e2": {
   "required": [
     (O+"VisitController.java", r"isBefore\(LocalDate\.now\(\)\)|\.isAfter\(LocalDate\.now\(\)\.minusDays\(1\)\)|!\s*visit\.getDate\(\)\.isBefore"),
     (O+"VisitController.java", r"minVisitDate\(\) \{\s*return LocalDate\.now\(\);"),
     ("src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java", r"LocalDate\.now\(\)\.minusDays\("),
     ("src/main/resources/messages/messages.properties", r"typeMismatch\.visitDate=(?!Visit date must be in the future)"),
     ("src/main/resources/messages/messages_pt.properties", r"typeMismatch\.visitDate=(?!A data da visita deve estar no futuro)"),
   ],
   "forbidden_changed": [],
   "max_files": 13,
 },
 "e3": {
   "required": [
     ("src/logic/createFormControl.ts", r"const handleFieldEvent: ChangeHandler = async \(event\)"),
     ("src/logic/createFormControl.ts", r"onChange: handleFieldEvent,"),
     ("src/logic/createFormControl.ts", r"onBlur: handleFieldEvent,"),
   ],
   "forbidden_changed": [
     ("src/logic/createFormControl.ts", r"reValidateMode: VALIDATION_MODE\.onChange,"),
     ("src/logic/createFormControl.ts", r"field\._f\.onChange\(event\)"),
   ],
   "max_files": 1,
 },
}
for name in sorted(os.listdir(os.path.join(SP,"edit"))):
    d = os.path.join(SP,"edit",name)
    scen = name.split("-")[0]
    c = checks[scen]
    changed = files(d)
    req = sum(1 for path,pat in c["required"] if has(d,path,pat))
    forb = sum(1 for path,pat in c["forbidden_changed"] if not has(d,path,pat))
    extra = len(changed) > c["max_files"]
    log = os.path.join(SP,"exp","logs",f"edit-{name}.log")
    calls, nbytes = 0, 0
    if os.path.exists(log):
        text = open(log,errors="replace").read()
        calls = text.count("### STATUS:")
        for m in re.finditer(r"### STATUS: \S+ BYTES: (\d+)", text): nbytes += int(m.group(1))
    print(f"{name:16s} required {req}/{len(c['required'])}  forbidden-touched {forb}  files changed {len(changed)}{' (EXTRA)' if extra else ''}  wrapper calls {calls}  bytes {nbytes}  -> {' '.join(changed)}")
