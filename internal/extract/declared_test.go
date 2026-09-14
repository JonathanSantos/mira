package extract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/JonathanSantos/mira/internal/lang"
)

func TestDeclaredType(t *testing.T) {
	tests := []struct {
		sig, name string
		callable  bool
		ts        bool
		want      string
	}{
		{"func (o *Owner) AddPet(p *Pet) error", "AddPet", true, false, "error"},
		{"func New(name string) *Owner", "New", true, false, "Owner"},
		{"func (r *Repo) All() ([]*Owner, error)", "All", true, false, "Owner[]"},
		{"func Find(id int) (o *Owner, err error)", "Find", true, false, "Owner"},
		{"pets []*Pet", "pets", false, false, "Pet[]"},
		{"Name string", "Name", false, false, "string"},
		{"def add_pet(self, pet: Pet, count=1) -> Pet", "add_pet", true, false, "Pet"},
		{"def all(self) -> list[Pet]", "all", true, false, "Pet[]"},
		{"def maybe(self) -> Optional[\"Pet\"]", "maybe", true, false, "Pet"},
		{"name: str", "name", false, false, "str"},
		{"public Pet getPet(Integer id)", "getPet", true, false, "Pet"},
		{"public static <T> List<T> of(T... items)", "of", true, false, "List<T>"},
		{"private final OwnerRepository owners", "owners", false, false, "OwnerRepository"},
		{"protected Page<Owner> findPaginated(int page)", "findPaginated", true, false, "Page<Owner>"},
		{"public Owner()", "Owner", true, false, ""},
		{"total(order: Order): number", "total", true, true, "number"},
		{"export function useForm<T>(props: UseFormProps<T> = {}): UseFormReturn<T>", "useForm", true, true, "UseFormReturn<T>"},
		{"const f = async (id: string): Promise<Pet> =>", "f", true, true, "Promise<Pet>"},
		{"const g = (x) =>", "g", true, true, ""},
		{"private readonly orders: OrderService", "orders", false, true, "OrderService"},
		{"cache = new Map()", "cache", false, true, ""},
		{"export const LIMIT: number = 3", "LIMIT", false, true, "number"},
	}
	for _, tt := range tests {
		t.Run(tt.sig, func(t *testing.T) {
			l := lang.Java
			switch {
			case tt.ts:
				l = lang.TypeScript
			case strings.HasPrefix(tt.sig, "func ") || strings.HasPrefix(tt.sig, "pets ") || strings.HasPrefix(tt.sig, "Name string"):
				l = lang.Go
			case strings.HasPrefix(tt.sig, "def ") || tt.sig == "name: str":
				l = lang.Python
			}
			assert.Equal(t, tt.want, DeclaredType(tt.sig, tt.name, tt.callable, l))
		})
	}
}
