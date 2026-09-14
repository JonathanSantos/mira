package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenario recria o que os agentes fizeram no teste de campo: a mesma
// pergunta respondida com mira e com grep/sed, os fatos do gabarito
// que cada saída tem de conter e a razão de tamanho entre as duas. Serve
// de regressão para "a ferramenta acha o que o grep acha" e "gasta menos".
type scenario struct {
	name      string
	mira      [][]string // uma chamada por item, sem --repo
	shell     []string   // comandos rodados com sh -c na raiz do repo
	facts     []string   // trechos que a saída do mira tem de conter
	shellFact []string   // trechos que a saída do shell tem de conter (o braço grep também acha)
	maxRatio  float64    // bytes mira / bytes shell aceitos; 0 = só registrar
}

// replay roda os dois braços e devolve as saídas concatenadas e os bytes.
func replay(t *testing.T, root string, sc scenario) (cg, sh string) {
	t.Helper()
	for _, args := range sc.mira {
		out, err := tryRun(root, append([]string{"--max-tokens", "4000"}, args...)...)
		require.NoError(t, err, "%s: mira %v\n%s", sc.name, args, out)
		cg += out
	}
	for _, cmd := range sc.shell {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = root
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s: %s\n%s", sc.name, cmd, out)
		sh += string(out)
	}
	return cg, sh
}

func check(t *testing.T, root string, sc scenario) {
	t.Helper()
	cg, sh := replay(t, root, sc)
	for _, f := range sc.facts {
		assert.Contains(t, cg, f, "%s: mira output lacks %q", sc.name, f)
	}
	for _, f := range sc.shellFact {
		assert.Contains(t, sh, f, "%s: shell output lacks %q", sc.name, f)
	}
	ratio := float64(len(cg)) / float64(len(sh))
	t.Logf("%-34s mira %2d calls %6d B | shell %2d cmds %6d B | ratio %.2f",
		sc.name, len(sc.mira), len(cg), len(sc.shell), len(sh), ratio)
	if sc.maxRatio > 0 {
		assert.LessOrEqual(t, ratio, sc.maxRatio, "%s: mira/shell size ratio", sc.name)
	}
}

// Fixtures: versões pequenas dos três tipos de pergunta do teste de campo.
func TestScenariosOnFixtures(t *testing.T) {
	javaRoot := indexed(t, "java-service")
	nodeRoot := indexed(t, "node-backend")
	const orderService = "src/main/java/com/acme/order/DefaultOrderService.java"
	const report = "src/main/java/com/acme/report/ReportService.java"

	check(t, javaRoot, scenario{
		name: "impact: who calls Discount.calculate",
		mira: [][]string{{"refs", "calculate", "--exclude-tests"}},
		shell: []string{
			"grep -rn 'calculate(' src/main --include='*.java'",
			"sed -n '19,33p' " + orderService,
			"sed -n '9,13p' " + report,
		},
		facts: []string{
			"targets:\n  com.acme.pricing.Discount.calculate method src/main/java/com/acme/pricing/Discount.java:6-11",
			"30 [method] in DefaultOrderService.total:19-33: Money result = Discount.calculate(sum, percent);",
			"12 [method] in ReportService.discounted:11-13: return calculate(base, 10);",
		},
		shellFact: []string{orderService + ":30:", report + ":12:"},
		maxRatio:  1.0,
	})

	check(t, nodeRoot, scenario{
		name: "trace: controller.total to calculateDiscount",
		mira: [][]string{
			{"resolve", "OrderController.total", "--include-skeleton"},
			{"resolve", "OrderService.total", "--include-skeleton"},
			{"resolve", "calculateDiscount", "--include-body"},
		},
		shell: []string{
			"grep -rn 'total' src --include='*.ts'",
			"sed -n '8,15p' src/orders/order.controller.ts",
			"sed -n '20,27p' src/orders/order.service.ts",
			"grep -rn 'calculateDiscount' src",
			"sed -n '12,14p' src/pricing/discount.ts",
		},
		facts: []string{
			"OrderService.total src/orders/order.service.ts:24 (14)",
			"calculateDiscount src/pricing/discount.ts:12 (26)",
			"calculateDiscount function src/pricing/discount.ts:12-14 [exported]",
			"13|   return roundMoney(applyCoupon(total, coupon));",
		},
		shellFact: []string{"src/pricing/discount.ts:12:export function calculateDiscount"},
	})

	check(t, javaRoot, scenario{
		name:  "understand: DefaultOrderService.total",
		mira:  [][]string{{"resolve", "DefaultOrderService.total", "--include-skeleton"}},
		shell: []string{"cat -n " + orderService},
		facts: []string{
			"skeleton: 15 lines, 1 branches, 1 loops",
			"23 return cached;",
			"32 return result;",
			"Discount.calculate src/main/java/com/acme/pricing/Discount.java:6 (30)",
		},
		shellFact: []string{"return cached;"},
		// Num arquivo de 46 linhas o esqueleto mais o corpo curto (15 linhas,
		// incluído por padrão) empata com o cat; o ganho aparece em arquivos
		// reais (T5 no cenário de campo: 0,30).
		maxRatio: 1.2,
	})
}

// Repositórios reais do teste de campo (docs/field-test-2026-09-14). Roda só
// com MIRA_FIELD_REPOS apontando para um diretório com os clones
// spring-petclinic e react-hook-form nos commits usados no campo; os fatos
// são linhas exatas do gabarito.
var fieldCommits = map[string]string{
	"spring-petclinic": "818c4136ea971c21674525f9053de0d9c7ad8cfe",
	"react-hook-form":  "38efe7b54d1171c2c40b444ccfbc26b8de0b4699",
}

func fieldRepo(t *testing.T, name string) string {
	t.Helper()
	base := os.Getenv("MIRA_FIELD_REPOS")
	if base == "" {
		t.Skip("set MIRA_FIELD_REPOS to a directory with the field repositories")
	}
	root := filepath.Join(base, name)
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skipf("%s: not a git checkout: %v", root, err)
	}
	if got := strings.TrimSpace(string(head)); got != fieldCommits[name] {
		t.Skipf("%s is at %s, scenarios were written for %s", name, got, fieldCommits[name])
	}
	run(t, root, "init")
	run(t, root, "index")
	return root
}

func TestScenariosOnFieldRepos(t *testing.T) {
	const owner = "src/main/java/org/springframework/samples/petclinic/owner/"
	petclinic := fieldRepo(t, "spring-petclinic")

	// T3: o braço grep é o que o agente baseline rodou (bench-t3-baseline.log).
	check(t, petclinic, scenario{
		name: "T3 impact: addPet/addVisit callers",
		mira: [][]string{{"refs", "addPet", "--exclude-tests"}, {"refs", "addVisit", "--exclude-tests"}},
		shell: []string{
			"grep -rn -E 'addPet|addVisit' src/main",
			"sed -n '88,210p' " + owner + "PetController.java",
			"sed -n '64,81p' " + owner + "VisitController.java",
			"sed -n '98,112p' " + owner + "VisitController.java",
			"sed -n '173,183p' " + owner + "Owner.java",
		},
		facts: []string{
			"103 [method] in PetController.initCreationForm:100-105",
			"125 [method] in PetController.processCreationForm:107-137",
			"197 [method] in PetController.updatePetDetails:186-200",
			"108 [method -> Owner.addVisit] in VisitController.processNewVisitForm:97-112",
			"79 [method -> Pet.addVisit] in VisitController.loadPetWithVisit:63-81",
			"182 [method -> Pet.addVisit] in Owner.addVisit:173-183",
		},
		shellFact: []string{"PetController.java:103:", "VisitController.java:108:", "Owner.java:182:"},
		maxRatio:  0.5,
	})

	// T4: as quatro chamadas do agente mira v3 contra o grep + 2 sed do baseline.
	check(t, petclinic, scenario{
		name: "T4 understand: processFindForm",
		mira: [][]string{
			{"resolve", "OwnerController.processFindForm", "--include-skeleton", "--include-body", "--exclude-tests"},
			{"resolve", "OwnerController.findPaginatedForOwnersLastName", "--include-skeleton", "--include-body", "--exclude-tests"},
			{"resolve", "OwnerController.addPaginationModel", "--include-skeleton", "--include-body", "--exclude-tests"},
			{"resolve", "OwnerRepository.findByLastNameStartingWith", "--include-body", "--exclude-tests"},
		},
		shell: []string{
			`grep -rn 'processFindForm\|private \|findByLastName\|PageRequest\|addPaginationModel\|throws\|^public class\|^class\|^interface\|^public interface' --include='*.java' ` + owner,
			"sed -n '86,138p' " + owner + "OwnerController.java",
			"sed -n '36,46p' " + owner + "OwnerRepository.java",
		},
		facts: []string{
			`111 return "owners/findOwners";`,
			`117 return "redirect:/owners/" + owner.getId();`,
			"121 return addPaginationModel(page, model, ownersResults);",
			"OwnerController.findPaginatedForOwnersLastName :133 (107)",
			"OwnerRepository.findByLastNameStartingWith " + owner + "OwnerRepository.java:45 (136)",
			"134| \t\tint pageSize = 5;",
			"OwnerController.owners field :53 (136)",
		},
		shellFact: []string{"int pageSize = 5;"},
		maxRatio:  0.8,
	})

	rhf := fieldRepo(t, "react-hook-form")

	// T5 como o agente rodou: esqueleto + 3 snippets que cobrem a função
	// inteira + 3 helpers + constantes, contra 4 cat + grep do baseline. Aqui
	// o mira NÃO ganha em bytes: a tarefa pede a função inteira.
	check(t, rhf, scenario{
		name: "T5 understand: validateField (as run)",
		mira: [][]string{
			{"resolve", "validateField", "--include-skeleton", "--path", "src/logic/validateField.ts", "--exclude-tests"},
			{"snippet", "src/logic/validateField.ts", "42", "130"},
			{"snippet", "src/logic/validateField.ts", "131", "235"},
			{"snippet", "src/logic/validateField.ts", "236", "301"},
			{"resolve", "getValueAndMessage", "--include-body", "--exclude-tests"},
			{"resolve", "getValidateError", "--include-body", "--exclude-tests"},
			{"resolve", "appendErrors", "--include-body", "--exclude-tests"},
			{"snippet", "src/constants.ts", "18", "30"},
		},
		shell: []string{
			"cat -n src/logic/validateField.ts",
			"cat -n src/logic/appendErrors.ts",
			"cat -n src/logic/getValidateError.ts",
			"cat -n src/logic/getValueAndMessage.ts",
			"grep -n -A 10 INPUT_VALIDATION_RULES src/constants.ts",
		},
		facts: []string{
			"58 return {};",
			"300 return error;",
			"nested (2): setCustomValidity 61-71, getMinMaxMessage 89-103",
			"getValueAndMessage src/logic/getValueAndMessage.ts:5 (116, 135, 136, 6 total)",
			"getValidateError src/logic/getValidateError.ts:5 (239, 262)",
			"callers (1):\n    validateField function src/logic/validateField.ts:34-301",
			"120|         type: INPUT_VALIDATION_RULES.required,",
		},
		shellFact: []string{"   120\t        type: INPUT_VALIDATION_RULES.required,"},
		maxRatio:  1.3,
	})

	check(t, rhf, scenario{
		name:     "T5 understand: validateField (skeleton only)",
		mira:     [][]string{{"resolve", "validateField", "--include-skeleton", "--exclude-tests"}},
		shell:    []string{"cat -n src/logic/validateField.ts"},
		facts:    []string{"returns (8):", "58 return {};", "300 return error;"},
		maxRatio: 0.5,
	})

	// T2 pela receita da skill: um resolve --include-skeleton por salto, snippets só
	// no fim; o braço grep é o núcleo do que o baseline rodou (9 dos 24 comandos).
	check(t, rhf, scenario{
		name: "T2 trace: register to required",
		mira: [][]string{
			{"resolve", "useForm", "--include-skeleton", "--exclude-tests"},
			{"resolve", "register", "--include-skeleton", "--exclude-tests", "--path", "src/logic"},
			{"resolve", "handleSubmit", "--include-skeleton", "--exclude-tests"},
			{"resolve", "executeBuiltInValidation", "--include-skeleton", "--exclude-tests"},
			{"resolve", "validateField", "--include-skeleton", "--exclude-tests"},
			{"snippet", "src/logic/validateField.ts", "105", "130"},
			{"snippet", "src/constants.ts", "18", "26"},
		},
		shell: []string{
			`grep -n 'register\|createFormControl\|handleSubmit' src/useForm.ts`,
			"sed -n '36,82p' src/useForm.ts",
			`grep -n 'const register\|const handleSubmit\|const executeBuiltInValidation\|validateField(' src/logic/createFormControl.ts`,
			"sed -n '1696,1830p' src/logic/createFormControl.ts",
			"sed -n '1827,1905p' src/logic/createFormControl.ts",
			"sed -n '747,830p' src/logic/createFormControl.ts",
			`grep -n 'required\|INPUT_VALIDATION_RULES' src/logic/validateField.ts`,
			"sed -n '34,131p' src/logic/validateField.ts",
			"sed -n '10,32p' src/constants.ts",
		},
		facts: []string{
			"createFormControl src/logic/createFormControl.ts:145 (71)",
			"createFormControl.register function src/logic/createFormControl.ts:1696-1798",
			"createFormControl.executeBuiltInValidation :747 (1858)",
			"validateField src/logic/validateField.ts:34 (",
			"120|         type: INPUT_VALIDATION_RULES.required,",
			"24|   required: 'required',",
		},
		shellFact: []string{"1696:  const register", "  required: 'required',"},
		maxRatio:  1.0,
	})
}
