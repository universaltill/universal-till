package catimport

// Built-in category/item icon library (ut-docs#2506, first cut).
//
// Every icon is generated from a vendored upstream SVG under iconsrc/
// (Lucide — ISC, Tabler — MIT; both LICENSE files sit next to the SVGs)
// into a 64×64 tile at web/public/assets/category-icons/<key>.svg by the
// golden-file test in icons_render_test.go. TestCategoryIconTiles_InSync
// fails when a tile, this registry and its source drift, so the files are
// never hand-edited — change the registry or the source, then regenerate:
//
//	UPDATE_CATEGORY_ICONS=1 go test ./internal/catimport -run TestCategoryIconTiles_InSync
//
// (The generator lives in a _test.go file so the shipped binary carries
// no code it never runs — guard-deadcode-baseline.sh.)
//
// Why a baked tile and not a bare `currentColor` glyph like
// internal/httpx/icons.go: these icons render through `<img src>` (item
// tiles on the sell screen, the pickers) and are stored as that path in
// item_images/categories, and an <img> can't inherit currentColor. So
// each tile carries its group's soft background and dark stroke, the same
// look the original coffee tile (ut-docs#1189) had.
//
// Keys are plain kebab-case because the key is the filename inside the
// stored path; Src records the upstream id ("lucide:beer") for the
// cross-surface registry the follow-up cards build on (my., kiosk,
// hosted shop pages). A key, once shipped, is never renamed or removed:
// shops' stored paths point at it.

// iconDef is one registry entry. Src is "<set>:<name>" naming
// iconsrc/<set>/<name>.svg, or "" for a hand-drawn legacy tile that the
// generator leaves alone. Keywords are English search terms for the
// picker's filter box (the translated label is searched too).
type iconDef struct {
	Key, Src, Group string
	Keywords        []string
}

// iconGroup is one picker section: its tile colours and heading key.
type iconGroup struct {
	Key, Background, Stroke string
}

// iconGroups is the picker's section order.
var iconGroups = []iconGroup{
	{Key: "hot", Background: "#F2E8DD", Stroke: "#6F4E37"},
	{Key: "cold", Background: "#E0F2FE", Stroke: "#075985"},
	{Key: "bar", Background: "#EDE9FE", Stroke: "#5B21B6"},
	{Key: "bakery", Background: "#FEF3C7", Stroke: "#92400E"},
	{Key: "meals", Background: "#FFE4E6", Stroke: "#9F1239"},
	{Key: "produce", Background: "#DCFCE7", Stroke: "#166534"},
	{Key: "sweets", Background: "#FCE7F3", Stroke: "#9D174D"},
	{Key: "general", Background: "#E2E8F0", Stroke: "#334155"},
}

// iconDefs is the library, in display order within each group. The five
// keys shipped before ut-docs#2506 (coffee, drink, sandwich, pastry,
// generic) keep their filenames; generic keeps its hand-drawn "no
// picture" art (Src "").
var iconDefs = []iconDef{
	// Hot drinks
	{"coffee", "lucide:coffee", "hot", []string{"coffee", "latte", "cappuccino", "americano", "flat white", "mocha"}},
	{"espresso", "tabler:coffee", "hot", []string{"espresso", "cup", "macchiato", "cortado"}},
	{"coffee-to-go", "tabler:cup", "hot", []string{"to go", "takeaway coffee", "coffee to go", "paper cup"}},
	{"tea", "tabler:teapot", "hot", []string{"tea", "chai", "herbal", "infusion", "pot"}},
	{"hot-chocolate", "tabler:mug", "hot", []string{"hot chocolate", "cocoa", "mug"}},
	// Cold drinks
	{"drink", "lucide:cup-soda", "cold", []string{"drink", "soda", "soft drink", "iced", "takeaway cup"}},
	{"can", "lucide:can-soda", "cold", []string{"can", "cola", "lemonade", "energy drink"}},
	{"water", "lucide:glass-water", "cold", []string{"water", "sparkling", "still"}},
	{"juice", "tabler:beer", "cold", []string{"juice", "orange juice", "glass"}},
	{"smoothie", "tabler:milkshake", "cold", []string{"smoothie", "milkshake", "shake", "frappe"}},
	{"milk", "lucide:milk", "cold", []string{"milk", "dairy", "oat milk"}},
	{"bottle", "tabler:bottle", "cold", []string{"bottle", "bottled"}},
	{"bubble-tea", "tabler:bubble-tea", "cold", []string{"bubble tea", "boba", "iced tea", "iced coffee"}},
	// Bar
	{"beer", "lucide:beer", "bar", []string{"beer", "lager", "ale", "pint", "draught", "bier"}},
	{"wine", "lucide:wine", "bar", []string{"wine", "red wine", "white wine", "rose", "glass of wine"}},
	{"wine-bottle", "lucide:bottle-wine", "bar", []string{"wine bottle", "bottle of wine"}},
	{"cocktail", "tabler:glass-cocktail", "bar", []string{"cocktail", "martini", "mojito", "margarita", "aperitif"}},
	{"spirits", "tabler:glass-gin", "bar", []string{"spirits", "shot", "whisky", "whiskey", "gin", "vodka", "rum"}},
	{"champagne", "tabler:glass-champagne", "bar", []string{"champagne", "prosecco", "sparkling wine", "sekt", "cava"}},
	// Bakery
	{"pastry", "tabler:cake-roll", "bakery", []string{"pastry", "pastries", "roll", "danish", "cinnamon roll"}},
	{"croissant", "lucide:croissant", "bakery", []string{"croissant", "viennoiserie"}},
	{"bread", "tabler:bread", "bakery", []string{"bread", "loaf", "bagel", "toast", "sourdough"}},
	{"baguette", "tabler:baguette", "bakery", []string{"baguette", "french stick", "pretzel"}},
	{"cake", "lucide:cake", "bakery", []string{"cake", "birthday cake", "gateau", "torte"}},
	{"cake-slice", "lucide:cake-slice", "bakery", []string{"slice of cake", "cheesecake", "pie", "tart"}},
	{"cupcake", "lucide:cupcake", "bakery", []string{"cupcake", "muffin"}},
	{"cookie", "lucide:cookie", "bakery", []string{"cookie", "biscuit"}},
	{"donut", "lucide:donut", "bakery", []string{"donut", "doughnut"}},
	// Meals
	{"sandwich", "lucide:sandwich", "meals", []string{"sandwich", "panini", "toastie", "club"}},
	{"burger", "lucide:hamburger", "meals", []string{"burger", "hamburger", "cheeseburger"}},
	{"pizza", "lucide:pizza", "meals", []string{"pizza", "slice"}},
	{"hot-dog", "tabler:sausage", "meals", []string{"hot dog", "sausage", "bratwurst", "currywurst"}},
	{"salad", "lucide:salad", "meals", []string{"salad", "bowl", "healthy"}},
	{"soup", "lucide:soup", "meals", []string{"soup", "stew", "broth"}},
	{"noodles", "tabler:bowl-chopsticks", "meals", []string{"noodles", "ramen", "rice", "sushi", "wok", "asian"}},
	{"bowl", "tabler:bowl-spoon", "meals", []string{"bowl", "porridge", "cereal", "muesli", "pasta"}},
	{"breakfast", "lucide:egg-fried", "meals", []string{"breakfast", "egg", "eggs", "brunch", "omelette"}},
	{"cheese", "tabler:cheese", "meals", []string{"cheese", "deli"}},
	{"meat", "lucide:beef", "meals", []string{"meat", "beef", "steak", "butcher"}},
	{"ham", "lucide:ham", "meals", []string{"ham", "pork", "bacon", "cold cuts"}},
	{"chicken", "lucide:drumstick", "meals", []string{"chicken", "wings", "poultry", "drumstick"}},
	{"fish", "lucide:fish", "meals", []string{"fish", "salmon", "fish and chips", "fishmonger"}},
	{"seafood", "lucide:shrimp", "meals", []string{"seafood", "shrimp", "prawn", "prawns"}},
	{"popcorn", "lucide:popcorn", "meals", []string{"popcorn", "snack", "snacks"}},
	{"meal", "lucide:utensils", "meals", []string{"meal", "dish", "main", "lunch", "dinner", "menu", "food"}},
	// Fruit & vegetables
	{"apple", "lucide:apple", "produce", []string{"apple", "fruit"}},
	{"banana", "lucide:banana", "produce", []string{"banana", "bananas"}},
	{"citrus", "lucide:citrus", "produce", []string{"citrus", "orange", "lemon", "lime", "grapefruit"}},
	{"berries", "lucide:cherry", "produce", []string{"berries", "cherry", "cherries", "strawberry", "strawberries"}},
	{"grapes", "lucide:grape", "produce", []string{"grapes", "grape"}},
	{"melon", "tabler:melon", "produce", []string{"melon", "watermelon"}},
	{"avocado", "tabler:avocado", "produce", []string{"avocado", "guacamole"}},
	{"carrot", "lucide:carrot", "produce", []string{"carrot", "vegetables", "veg"}},
	{"greens", "lucide:leafy-green", "produce", []string{"greens", "lettuce", "spinach", "cabbage", "herbs"}},
	{"pepper", "tabler:pepper", "produce", []string{"pepper", "peppers", "chili", "chilli", "paprika"}},
	{"mushroom", "tabler:mushroom", "produce", []string{"mushroom", "mushrooms"}},
	{"nuts", "lucide:nut", "produce", []string{"nuts", "nut", "almond", "hazelnut"}},
	// Sweets & ice cream
	{"ice-cream", "lucide:ice-cream-cone", "sweets", []string{"ice cream", "gelato", "cone", "scoop"}},
	{"sundae", "lucide:ice-cream-bowl", "sweets", []string{"sundae", "frozen yoghurt", "dessert"}},
	{"ice-lolly", "lucide:popsicle", "sweets", []string{"ice lolly", "popsicle"}},
	{"candy", "lucide:candy", "sweets", []string{"candy", "sweets", "confectionery"}},
	{"lollipop", "lucide:lollipop", "sweets", []string{"lollipop"}},
	{"chocolate", "tabler:chocolate", "sweets", []string{"chocolate", "chocolate bar", "praline"}},
	// General
	{"generic", "", "general", []string{"item", "product", "other"}},
	{"tag", "lucide:tag", "general", []string{"tag", "label", "price"}},
	{"gift", "lucide:gift", "general", []string{"gift", "present", "voucher"}},
	{"bag", "lucide:shopping-bag", "general", []string{"bag", "shopping", "takeaway"}},
	{"box", "lucide:package", "general", []string{"box", "package", "parcel"}},
	{"star", "lucide:star", "general", []string{"star", "special", "favourite", "favorite"}},
	{"offer", "lucide:percent", "general", []string{"offer", "discount", "sale", "deal", "percent"}},
	{"vegan", "lucide:vegan", "general", []string{"vegan", "vegetarian", "plant based"}},
	{"gluten-free", "lucide:wheat-off", "general", []string{"gluten free", "gluten-free", "gf"}},
}

// iconByKey indexes iconDefs; built once at init.
var iconByKey = func() map[string]iconDef {
	m := make(map[string]iconDef, len(iconDefs))
	for _, d := range iconDefs {
		m[d.Key] = d
	}
	return m
}()

// categoryIconPublicDir is where the tiles are served from ("/public/..."
// maps to web/public/ — the same convention the per-item uploads use).
const categoryIconPublicDir = "/public/assets/category-icons/"

// BuiltinIconGroup is one section of the picker: its heading's locale key
// and its icons in display order.
type BuiltinIconGroup struct {
	Key, I18nKey string
	Icons        []BuiltinIcon
}

// BuiltinIconGroups returns the library grouped for the pickers, in
// section order. Every icon in BuiltinIcons() appears in exactly one
// group.
func BuiltinIconGroups() []BuiltinIconGroup {
	byGroup := map[string][]BuiltinIcon{}
	for _, ic := range BuiltinIcons() {
		byGroup[ic.Group] = append(byGroup[ic.Group], ic)
	}
	out := make([]BuiltinIconGroup, 0, len(iconGroups))
	for _, g := range iconGroups {
		out = append(out, BuiltinIconGroup{
			Key:     g.Key,
			I18nKey: "catalog.builtin_icon.group." + g.Key,
			Icons:   byGroup[g.Key],
		})
	}
	return out
}
