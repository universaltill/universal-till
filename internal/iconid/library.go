package iconid

// The till's category icon library (ut-docs#2506), the ONE list both
// halves read (ut-docs#2664/#2717): this package's id registry (what an
// icon id from my./a directive draws) and internal/catimport's picker,
// tile generator and keyword placeholder. It lives here, not in catimport,
// because catimport imports internal/data, which imports this package —
// the data has to sit at the leaf of that chain.
//
// Each entry is a tile at web/public/assets/category-icons/<Key>.svg
// generated from the vendored upstream glyph named by ID (Lucide — ISC,
// Tabler — MIT; catimport/iconsrc/<set>/<name>.svg with the LICENSE files
// beside them). ID is that upstream glyph's own name, so it is also the
// contract's icon id (manage-shop-catalog-api §0.12) — the id my.'s picker
// stores for the same artwork. Rules, all pinned by tests:
//   - a Key, once shipped, is never renamed or removed: shops' stored
//     image paths point at it (older tills wrote the path, not the id);
//   - an ID is unique and never reused for different artwork;
//   - only the hand-drawn legacy "generic" tile has no ID.

// LibraryIcon is one library tile. Keywords are English search terms for
// the pickers' filter box; Group is the picker section (catimport owns the
// sections' colours and headings).
type LibraryIcon struct {
	Key, ID, Group string
	Keywords       []string
}

// PublicDir is where the tiles are served from ("/public/..." maps to
// web/public/).
const PublicDir = "/public/assets/category-icons/"

// Library returns the whole library in display order (a copy).
func Library() []LibraryIcon {
	out := make([]LibraryIcon, len(library))
	copy(out, library)
	return out
}

// library is the list, in display order within each group. The five keys
// shipped before ut-docs#2506 (coffee, drink, sandwich, pastry, generic)
// keep their filenames.
var library = []LibraryIcon{
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
	{"leaf", "lucide:leaf", "produce", []string{"leaf", "organic", "plant", "tea leaf", "natural"}},
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
