# Gabaritos (montados com grep antes de lançar os agentes)

Pontuação por fato: 1 = afirmado com arquivo e range de linhas que cobre a linha real;
0,5 = afirmação correta com citação errada ou ausente; 0 = ausente ou errado.
Alucinação = afirmação contradita pelo código (conta à parte).

## T1 petclinic — fluxo de criação de visita (8 fatos)
1. POST em VisitController.processNewVisitForm, VisitController.java:97-112 (@PostMapping linha 97)
2. loadPetWithVisit (@ModelAttribute) VisitController.java:63-81 carrega Owner via owners.findById (66)
3. Pet via owner.getPet(petId) (VisitController ~70; Owner.getPet(Integer) Owner.java:126-136)
4. pet.addVisit(visit) em loadPetWithVisit, linha 79
5. owner.addVisit(petId, visit) linha 108 -> Owner.addVisit Owner.java:173-183 -> Pet.addVisit Pet.java:81-83
6. Persistência: this.owners.save(owner) linha 109 (herdado de JpaRepository; cascade @OneToMany Owner.java:64-66 / Pet.java:56-59)
7. Validação: @Valid Visit (98) + @NotBlank description Visit.java:42-43; regra da data result.rejectValue("date", ...) 100-102; hasErrors 104-106
8. @InitBinder setAllowedFields proíbe id, VisitController.java:51-54

## T2 react-hook-form — register('email', {required:true}) até a validação (8 fatos)
1. useForm chama createFormControl(props) em src/useForm.ts:71 e devolve _formControl.current (193)
2. register implementado em src/logic/createFormControl.ts:1696-1798
3. gravação: set(_fields, name, { ..., _f: { ..., ...options } }) em 1703-1711 (options.required vai para _f); _fields declarado em 174
4. handleSubmit em src/logic/createFormControl.ts:1827-1901
5. handleSubmit chama executeBuiltInValidation({ fields: _fields, eventType: EVENTS.SUBMIT }) em ~1858-1861
6. executeBuiltInValidation 747-852 itera os campos e chama validateField em 800-808
7. validateField (export default) src/logic/validateField.ts:34-301; checagem de required em 105-130 (108 `required &&`, 120 type: INPUT_VALIDATION_RULES.required)
8. INPUT_VALIDATION_RULES.required = 'required' em src/constants.ts:18-26 (linha 24)

## T3 petclinic — impacto: quem chama Owner.addPet, Owner.addVisit ou Pet.addVisit em src/main (6 call sites)
1. PetController.initCreationForm PetController.java:100-105, chamada owner.addPet(pet) na 103
2. PetController.processCreationForm PetController.java:107-137, chamada na 125
3. PetController.updatePetDetails PetController.java:186-200, chamada na 197
4. VisitController.processNewVisitForm VisitController.java:97-112, owner.addVisit(petId, visit) na 108
5. VisitController.loadPetWithVisit VisitController.java:63-81, pet.addVisit(visit) na 79
6. Owner.addVisit Owner.java:173-183, pet.addVisit(visit) na 182
Falso positivo = qualquer call site fora dessa lista (ex.: testes, quando a tarefa pede src/main).

Nota: o conteúdo acima é o que foi fixado na conversa antes de lançar os agentes;
o arquivo em si foi gravado depois porque o comando que o criava falhou num glob.

## T4 petclinic — entender OwnerController.processFindForm (9 fatos)
Método: OwnerController.java:94-122 (@GetMapping("/owners") na 94).
1. `return "owners/findOwners"` na 111, quando `ownersResults.isEmpty()` (108-112), depois de `result.rejectValue("lastName", "notFound", "not found")` na 110
2. `return "redirect:/owners/" + owner.getId()` na 117, quando `getTotalElements() == 1` (114-118)
3. `return addPaginationModel(page, model, ownersResults)` na 121 (caso de vários), que devolve `"owners/ownersList"` na 130
4. lastName: `owner.getLastName()` na 98; null vira "" (99-101), senão `strip()` (103)
5. `findPaginatedForOwnersLastName` OwnerController.java:133-137, chama `owners.findByLastNameStartingWith(lastname, pageable)` na 136
6. paginação: `pageSize = 5` (134) e `PageRequest.of(page - 1, pageSize)` (135)
7. `OwnerRepository.findByLastNameStartingWith` declarado em OwnerRepository.java:45
8. `addPaginationModel` 124-131 põe currentPage, totalPages, totalItems e listOwners no model (126-129)
9. nenhuma exceção declarada nem lançada no método (94-122)

## T5 react-hook-form — entender validateField (8 fatos)
Função: src/logic/validateField.ts:34-301, `export default async` com (field, disabledFieldNames, formValues, validateAllFieldCriteria, shouldUseNativeValidation?, isFieldArray?).
1. saída antecipada `return {}` na 58 quando `!mount || disabledFieldNames.has(name)` (57-59)
2. ordem das regras: required (105-130) → min/max (132-190) → maxLength/minLength (195-216) → pattern (218-233) → validate (236-293); retorno final `return error` na 300
3. quando `!validateAllFieldCriteria` cada regra devolve no primeiro erro: 127, 187, 214, 231, 251, 290 (com `setCustomValidity` antes)
4. tipos de erro vêm de `INPUT_VALIDATION_RULES` (src/constants.ts:18-26): required (120), max/min (182-183), maxLength/minLength via defaults maxType/minType (93-94), pattern (224), validate (245) ou a chave do objeto (271)
5. `appendErrorsCurry = appendErrors.bind(null, name, validateAllFieldCriteria, error)` (83-88) acumula os erros quando validateAllFieldCriteria é true
6. `getValueAndMessage` (src/logic/getValueAndMessage.ts) normaliza valor/mensagem das regras (116, 135-136, 197-198, 220); `getValidateError` (src/logic/getValidateError.ts) converte o resultado de validate (239, 262)
7. validate função: 237-253, chama `validate(inputValue, formValues)` na 238; validate objeto: 254-293, itera `for (const key in validate)` na 257 e chama `validate[key]` na 263
8. validação nativa: `setCustomValidity` (61-71) chama `inputRef.reportValidity()` quando `shouldUseNativeValidation` (62)

## E1 petclinic — rename Owner.addVisit → scheduleVisit (edição)
Obrigatório (3): Owner.java:173 definição renomeada; VisitController.java:108 `owner.scheduleVisit(petId, visit)`; ClinicServiceTests.java:229 `owner6.scheduleVisit(...)`.
Proibido (3): Pet.java:81 `Pet.addVisit` intacto; VisitController.java:79 `pet.addVisit(visit)` intacto; Owner.java:182 `pet.addVisit(visit)` intacto. Nenhum outro arquivo.

## E2 petclinic — aceitar visita hoje (edição)
Obrigatório (5): VisitController.java:100 condição vira "data no passado" (`isBefore(LocalDate.now())`); VisitController.java:85 `minVisitDate` vira hoje (`LocalDate.now()`); VisitControllerTests.java:101 caso inválido passa a usar data passada (`minusDays(1)`); messages.properties:9 texto em inglês atualizado; messages_pt.properties:9 texto em português atualizado.
Opcional: demais idiomas. Template `inputField.html` não muda (usa `minVisitDate`).

## E3 react-hook-form — rename da closure onChange → handleFieldEvent (edição)
Obrigatório (3): createFormControl.ts:1174 `const handleFieldEvent: ChangeHandler`; 1740 `onChange: handleFieldEvent,` (a chave pública continua `onChange`); 1741 `onBlur: handleFieldEvent,`.
Proibido: 112 `VALIDATION_MODE.onChange`; 1220-1221 `field._f.onChange`; qualquer outro arquivo (há ~300 `onChange` em src).
