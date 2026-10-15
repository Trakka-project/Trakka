package catalog

import "strings"

// The keyword tables below are in Key form (lowercase, unaccented, words
// separated by single spaces) and in the singular: lookups try a token as
// written, then without a trailing "s"/"x" (see singular).
//
// They are a first guess, not a classification: a household teaches the
// catalog its own aisles (house_products.aisle) as soon as someone files an
// item under one, and a learned aisle always wins over these tables (see
// internal/handlers.resolveItemAisle).

// exactAisles matches a whole name only — for words too ambiguous to match
// inside a longer name ("thé" folds to "the", the English article).
var exactAisles = map[string]string{
	"the": AisleBreakfast,
}

// phraseAisles matches a run of two to four words anywhere in a name, and is
// checked before any single word: "lait de coco" is not milk, "eau de
// javel" is not water, "crème solaire" is not cream.
var phraseAisles = map[string]string{
	// Produce
	"pomme de terre": AisleProduce, "haricot vert": AisleProduce, "chou fleur": AisleProduce,
	"citron vert": AisleProduce, "fine herbe": AisleProduce, "salade verte": AisleProduce,
	"sweet potato": AisleProduce, "spring onion": AisleProduce, "green bean": AisleProduce,
	// Bakery
	"pain de mie": AisleBakery, "pain au chocolat": AisleBakery, "pain complet": AisleBakery,
	// Meat & fish
	"blanc de poulet": AisleMeatFish, "steak hache": AisleMeatFish, "cordon bleu": AisleMeatFish,
	"chicken breast": AisleMeatFish, "ground beef": AisleMeatFish, "fruits de mer": AisleMeatFish,
	// Dairy & chilled
	"creme fraiche": AisleDairy, "fromage blanc": AisleDairy, "petit suisse": AisleDairy,
	"creme dessert": AisleDairy, "pate feuilletee": AisleDairy, "pate brisee": AisleDairy,
	"pate sablee": AisleDairy, "pate a pizza": AisleDairy, "sour cream": AisleDairy,
	"cream cheese": AisleDairy, "lait d amande": AisleDairy, "lait d avoine": AisleDairy,
	"lait de soja": AisleDairy,
	// Pantry
	"lait de coco": AislePantry, "huile d olive": AislePantry, "sauce tomate": AislePantry,
	"concentre de tomate": AislePantry, "pois chiche": AislePantry, "haricot rouge": AislePantry,
	"herbe de provence": AislePantry, "olive oil": AislePantry, "coconut milk": AislePantry,
	"tomato sauce": AislePantry, "soy sauce": AislePantry, "sauce soja": AislePantry,
	// Breakfast & sweets
	"pate a tartiner": AisleBreakfast, "pain d epice": AisleBreakfast, "corn flake": AisleBreakfast,
	"peanut butter": AisleBreakfast, "beurre de cacahuete": AisleBreakfast, "sirop d erable": AisleBreakfast,
	"maple syrup": AisleBreakfast, "flocon d avoine": AisleBreakfast, "creme de marron": AisleBreakfast,
	"the vert": AisleBreakfast, "the noir": AisleBreakfast, "sachet de the": AisleBreakfast,
	// Drinks
	"jus d orange": AisleDrinks, "eau gazeuse": AisleDrinks, "eau minerale": AisleDrinks,
	"orange juice": AisleDrinks, "sparkling water": AisleDrinks, "ice tea": AisleDrinks,
	"the glace": AisleDrinks, "vin rouge": AisleDrinks, "vin blanc": AisleDrinks,
	// Household
	"papier toilette": AisleHousehold, "essuie tout": AisleHousehold, "liquide vaisselle": AisleHousehold,
	"produit vaisselle": AisleHousehold, "lave vaisselle": AisleHousehold, "sac poubelle": AisleHousehold,
	"film alimentaire": AisleHousehold, "papier alu": AisleHousehold, "papier aluminium": AisleHousehold,
	"papier cuisson": AisleHousehold, "eau de javel": AisleHousehold, "toilet paper": AisleHousehold,
	"paper towel": AisleHousehold, "dish soap": AisleHousehold, "trash bag": AisleHousehold,
	"bin bag": AisleHousehold, "aluminium foil": AisleHousehold, "kitchen roll": AisleHousehold,
	// Health & beauty
	"gel douche": AisleHygiene, "brosse a dent": AisleHygiene, "coton tige": AisleHygiene,
	"apres shampoing": AisleHygiene, "creme solaire": AisleHygiene, "creme hydratante": AisleHygiene,
	"eau micellaire": AisleHygiene, "serviette hygienique": AisleHygiene, "shower gel": AisleHygiene,
	"cotton bud": AisleHygiene,
	// Baby
	"lait infantile": AisleBaby, "lait bebe": AisleBaby, "petit pot": AisleBaby,
	"baby food": AisleBaby, "baby wipe": AisleBaby,
	// Pets
	"cat food": AislePets, "dog food": AislePets, "cat litter": AislePets,
	"nourriture chat": AislePets, "nourriture chien": AislePets,
	// Frozen
	"ice cream": AisleFrozen, "ice cube": AisleFrozen, "frite surgelee": AisleFrozen,
}

// productWords name what a product *is* ("yaourt", "jus", "sauce"); they
// win over ingredientWords wherever they appear in the name, so "yaourt à
// la fraise" is dairy and "jus d'ananas" is a drink, not produce.
var productWords = map[string]string{
	// Bakery
	"pain": AisleBakery, "baguette": AisleBakery, "brioche": AisleBakery, "croissant": AisleBakery,
	"viennoiserie": AisleBakery, "tarte": AisleBakery, "pita": AisleBakery,
	"bread": AisleBakery, "bagel": AisleBakery, "loaf": AisleBakery,
	// Meat & fish
	"poulet": AisleMeatFish, "boeuf": AisleMeatFish, "porc": AisleMeatFish, "veau": AisleMeatFish,
	"agneau": AisleMeatFish, "dinde": AisleMeatFish, "canard": AisleMeatFish, "jambon": AisleMeatFish,
	"saucisse": AisleMeatFish, "saucisson": AisleMeatFish, "lardon": AisleMeatFish, "chorizo": AisleMeatFish,
	"steak": AisleMeatFish, "viande": AisleMeatFish, "escalope": AisleMeatFish, "merguez": AisleMeatFish,
	"bacon": AisleMeatFish, "poisson": AisleMeatFish, "saumon": AisleMeatFish, "cabillaud": AisleMeatFish,
	"crevette": AisleMeatFish, "moule": AisleMeatFish, "colin": AisleMeatFish, "merlu": AisleMeatFish,
	"truite": AisleMeatFish, "charcuterie": AisleMeatFish, "rillette": AisleMeatFish,
	"chicken": AisleMeatFish, "beef": AisleMeatFish, "pork": AisleMeatFish, "ham": AisleMeatFish,
	"sausage": AisleMeatFish, "turkey": AisleMeatFish, "lamb": AisleMeatFish, "fish": AisleMeatFish,
	"salmon": AisleMeatFish, "shrimp": AisleMeatFish, "prawn": AisleMeatFish, "meat": AisleMeatFish,
	"mince": AisleMeatFish, "cod": AisleMeatFish,
	// Dairy & chilled
	"lait": AisleDairy, "yaourt": AisleDairy, "yoghourt": AisleDairy, "yogourt": AisleDairy,
	"fromage": AisleDairy, "beurre": AisleDairy, "creme": AisleDairy, "oeuf": AisleDairy,
	"emmental": AisleDairy, "comte": AisleDairy, "camembert": AisleDairy, "mozzarella": AisleDairy,
	"parmesan": AisleDairy, "gruyere": AisleDairy, "chevre": AisleDairy, "brie": AisleDairy,
	"roquefort": AisleDairy, "raclette": AisleDairy, "feta": AisleDairy, "ricotta": AisleDairy,
	"mascarpone": AisleDairy, "skyr": AisleDairy, "kefir": AisleDairy, "margarine": AisleDairy,
	"milk": AisleDairy, "yogurt": AisleDairy, "yoghurt": AisleDairy, "cheese": AisleDairy,
	"butter": AisleDairy, "cream": AisleDairy, "egg": AisleDairy,
	// Pantry
	"pates": AislePantry, "riz": AislePantry, "farine": AislePantry, "huile": AislePantry,
	"vinaigre": AislePantry, "sel": AislePantry, "poivre": AislePantry, "epice": AislePantry,
	"conserve": AislePantry, "sauce": AislePantry, "ketchup": AislePantry, "mayonnaise": AislePantry,
	"moutarde": AislePantry, "bouillon": AislePantry, "soupe": AislePantry, "lentille": AislePantry,
	"semoule": AislePantry, "quinoa": AislePantry, "couscous": AislePantry, "nouille": AislePantry,
	"spaghetti": AislePantry, "tagliatelle": AislePantry, "penne": AislePantry, "coquillette": AislePantry,
	"lasagne": AislePantry, "chips": AislePantry, "cacahuete": AislePantry, "olive": AislePantry,
	"cornichon": AislePantry, "thon": AislePantry, "sardine": AislePantry, "mais": AislePantry,
	"tortilla": AislePantry, "pasta": AislePantry, "rice": AislePantry, "flour": AislePantry,
	"oil": AislePantry, "vinegar": AislePantry, "salt": AislePantry, "spice": AislePantry,
	"mayo": AislePantry, "mustard": AislePantry, "stock": AislePantry, "soup": AislePantry,
	"noodle": AislePantry, "crisps": AislePantry, "tuna": AislePantry,
	// Breakfast & sweets
	"cafe": AisleBreakfast, "chocolat": AisleBreakfast, "cacao": AisleBreakfast, "confiture": AisleBreakfast,
	"miel": AisleBreakfast, "cereale": AisleBreakfast, "muesli": AisleBreakfast, "granola": AisleBreakfast,
	"biscuit": AisleBreakfast, "gateau": AisleBreakfast, "sucre": AisleBreakfast, "compote": AisleBreakfast,
	"madeleine": AisleBreakfast, "bonbon": AisleBreakfast, "nutella": AisleBreakfast, "tisane": AisleBreakfast,
	"infusion": AisleBreakfast, "biscotte": AisleBreakfast, "coffee": AisleBreakfast, "tea": AisleBreakfast,
	"chocolate": AisleBreakfast, "jam": AisleBreakfast, "honey": AisleBreakfast, "cereal": AisleBreakfast,
	"cookie": AisleBreakfast, "sugar": AisleBreakfast, "candy": AisleBreakfast, "sweets": AisleBreakfast,
	"cocoa": AisleBreakfast, "oats": AisleBreakfast,
	// Drinks
	"eau": AisleDrinks, "jus": AisleDrinks, "soda": AisleDrinks, "coca": AisleDrinks,
	"limonade": AisleDrinks, "sirop": AisleDrinks, "biere": AisleDrinks, "vin": AisleDrinks,
	"champagne": AisleDrinks, "cidre": AisleDrinks, "alcool": AisleDrinks, "whisky": AisleDrinks,
	"rhum": AisleDrinks, "vodka": AisleDrinks, "water": AisleDrinks, "juice": AisleDrinks,
	"beer": AisleDrinks, "wine": AisleDrinks, "cola": AisleDrinks, "lemonade": AisleDrinks,
	// Household
	"lessive": AisleHousehold, "adoucissant": AisleHousehold, "eponge": AisleHousehold,
	"javel": AisleHousehold, "nettoyant": AisleHousehold, "detergent": AisleHousehold,
	"sopalin": AisleHousehold, "poubelle": AisleHousehold, "ampoule": AisleHousehold,
	"pile": AisleHousehold, "allumette": AisleHousehold, "bougie": AisleHousehold,
	"vaisselle": AisleHousehold, "detartrant": AisleHousehold, "desinfectant": AisleHousehold,
	"serpillere": AisleHousehold, "laundry": AisleHousehold, "sponge": AisleHousehold,
	"bleach": AisleHousehold, "cleaner": AisleHousehold, "batteries": AisleHousehold,
	"battery": AisleHousehold, "candle": AisleHousehold, "foil": AisleHousehold,
	// Health & beauty
	"dentifrice": AisleHygiene, "shampoing": AisleHygiene, "shampooing": AisleHygiene,
	"savon": AisleHygiene, "deodorant": AisleHygiene, "rasoir": AisleHygiene, "coton": AisleHygiene,
	"mouchoir": AisleHygiene, "maquillage": AisleHygiene, "toothpaste": AisleHygiene,
	"shampoo": AisleHygiene, "soap": AisleHygiene, "razor": AisleHygiene, "tissues": AisleHygiene,
	"conditioner": AisleHygiene, "lotion": AisleHygiene, "sunscreen": AisleHygiene,
	// Baby
	"couche": AisleBaby, "lingette": AisleBaby, "biberon": AisleBaby, "diaper": AisleBaby,
	"nappy": AisleBaby, "nappies": AisleBaby,
	// Pets
	"croquette": AislePets, "litiere": AislePets, "patee": AislePets, "kibble": AislePets,
	// Frozen
	"surgele": AisleFrozen, "surgelee": AisleFrozen, "glace": AisleFrozen, "glacon": AisleFrozen,
	"sorbet": AisleFrozen, "frozen": AisleFrozen,
}

// ingredientWords are fresh produce: matched only when no productWords
// word is in the name ("tomates" is produce, "sauce tomate" is not).
var ingredientWords = map[string]string{
	"pomme": AisleProduce, "poire": AisleProduce, "banane": AisleProduce, "orange": AisleProduce,
	"citron": AisleProduce, "clementine": AisleProduce, "mandarine": AisleProduce, "fraise": AisleProduce,
	"framboise": AisleProduce, "myrtille": AisleProduce, "cerise": AisleProduce, "peche": AisleProduce,
	"abricot": AisleProduce, "prune": AisleProduce, "raisin": AisleProduce, "kiwi": AisleProduce,
	"ananas": AisleProduce, "mangue": AisleProduce, "melon": AisleProduce, "pasteque": AisleProduce,
	"avocat": AisleProduce, "tomate": AisleProduce, "carotte": AisleProduce, "courgette": AisleProduce,
	"aubergine": AisleProduce, "poivron": AisleProduce, "concombre": AisleProduce, "salade": AisleProduce,
	"laitue": AisleProduce, "epinard": AisleProduce, "chou": AisleProduce, "brocoli": AisleProduce,
	"poireau": AisleProduce, "oignon": AisleProduce, "ail": AisleProduce, "echalote": AisleProduce,
	"champignon": AisleProduce, "radis": AisleProduce, "betterave": AisleProduce, "celeri": AisleProduce,
	"fenouil": AisleProduce, "navet": AisleProduce, "potiron": AisleProduce, "courge": AisleProduce,
	"butternut": AisleProduce, "patate": AisleProduce, "persil": AisleProduce, "basilic": AisleProduce,
	"coriandre": AisleProduce, "menthe": AisleProduce, "ciboulette": AisleProduce, "gingembre": AisleProduce,
	"legume": AisleProduce, "fruit": AisleProduce, "apple": AisleProduce, "pear": AisleProduce,
	"banana": AisleProduce, "lemon": AisleProduce, "lime": AisleProduce, "strawberry": AisleProduce,
	"raspberry": AisleProduce, "blueberry": AisleProduce, "cherry": AisleProduce, "peach": AisleProduce,
	"grape": AisleProduce, "pineapple": AisleProduce, "mango": AisleProduce, "avocado": AisleProduce,
	"tomato": AisleProduce, "tomatoes": AisleProduce, "carrot": AisleProduce, "zucchini": AisleProduce,
	"eggplant": AisleProduce, "cucumber": AisleProduce, "lettuce": AisleProduce, "spinach": AisleProduce,
	"cabbage": AisleProduce, "broccoli": AisleProduce, "leek": AisleProduce, "onion": AisleProduce,
	"garlic": AisleProduce, "mushroom": AisleProduce, "potato": AisleProduce, "potatoes": AisleProduce,
	"parsley": AisleProduce, "basil": AisleProduce, "ginger": AisleProduce, "vegetable": AisleProduce,
}

// singular strips one trailing "s" or "x" from a word long enough for that
// to be a plural mark ("tomates" → "tomate", "choux" → "chou"), leaving
// short words alone ("jus", "riz").
func singular(word string) string {
	if len(word) > 3 && (strings.HasSuffix(word, "s") || strings.HasSuffix(word, "x")) {
		return word[:len(word)-1]
	}
	return word
}

// lookupWord finds word in table as written, then in the singular.
func lookupWord(table map[string]string, word string) (string, bool) {
	if aisle, ok := table[word]; ok {
		return aisle, true
	}
	aisle, ok := table[singular(word)]
	return aisle, ok
}

// lookupPhrase finds the words in phraseAisles as written, then with every
// word in the singular.
func lookupPhrase(words []string) (string, bool) {
	if aisle, ok := phraseAisles[strings.Join(words, " ")]; ok {
		return aisle, true
	}
	singulars := make([]string, len(words))
	for i, w := range words {
		singulars[i] = singular(w)
	}
	aisle, ok := phraseAisles[strings.Join(singulars, " ")]
	return aisle, ok
}

// GuessAisle returns the aisle a product name most likely belongs to, or ""
// when nothing in it is recognized. Checked in order: the whole name
// (exactAisles), then multi-word phrases, longest and leftmost first, then
// the leftmost product word, then the leftmost produce word. "Leftmost"
// suits French, where the head noun comes first ("yaourt à la fraise",
// "jus d'orange"); English names are covered mostly by the product/produce
// split ("strawberry yogurt": yogurt is a product word, strawberry only an
// ingredient).
func GuessAisle(name string) string {
	key := Key(name)
	if key == "" {
		return ""
	}
	if aisle, ok := exactAisles[key]; ok {
		return aisle
	}
	words := strings.Fields(key)
	for size := min(4, len(words)); size >= 2; size-- {
		for start := 0; start+size <= len(words); start++ {
			if aisle, ok := lookupPhrase(words[start : start+size]); ok {
				return aisle
			}
		}
	}
	for _, w := range words {
		if aisle, ok := lookupWord(productWords, w); ok {
			return aisle
		}
	}
	for _, w := range words {
		if aisle, ok := lookupWord(ingredientWords, w); ok {
			return aisle
		}
	}
	return ""
}

// categoryRules maps Open Food Facts category tags (categories_tags, e.g.
// "en:yogurts") to an aisle. Checked in this order against every tag a
// product carries, so a more specific rule placed first wins over a broader
// one: Open Food Facts files frozen vegetables under both "en:frozen-foods"
// and "en:vegetables", coffee under "en:beverages", canned vegetables under
// "en:vegetables".
var categoryRules = []struct {
	aisle string
	tags  []string
}{
	{AisleFrozen, []string{"en:frozen-foods", "en:ice-creams", "en:frozen-desserts"}},
	{AisleBaby, []string{"en:baby-foods", "en:baby-milks"}},
	{AislePets, []string{"en:pet-food", "en:cat-food", "en:dog-food"}},
	{AisleDairy, []string{"en:dairies", "en:cheeses", "en:yogurts", "en:milks", "en:butters", "en:creams", "en:eggs", "en:fresh-desserts", "en:dairy-desserts"}},
	{AisleMeatFish, []string{"en:meats", "en:poultries", "en:fishes", "en:seafood", "en:prepared-meats", "en:hams", "en:sausages"}},
	{AisleBakery, []string{"en:breads", "en:viennoiseries", "en:pastries"}},
	{AisleBreakfast, []string{"en:breakfasts", "en:coffees", "en:teas", "en:herbal-teas", "en:breakfast-cereals", "en:biscuits", "en:biscuits-and-cakes", "en:chocolates", "en:sweet-spreads", "en:jams", "en:honeys", "en:confectioneries", "en:sugars", "en:sweet-snacks", "en:compotes"}},
	{AisleDrinks, []string{"en:beverages", "en:waters", "en:sodas", "en:juices", "en:fruit-juices", "en:alcoholic-beverages", "en:wines", "en:beers"}},
	{AislePantry, []string{"en:canned-foods", "en:pastas", "en:rices", "en:sauces", "en:condiments", "en:vegetable-oils", "en:spices", "en:soups", "en:salty-snacks", "en:legumes", "en:flours", "en:groceries", "en:cereals-and-potatoes", "en:meals"}},
	{AisleProduce, []string{"en:fresh-fruits", "en:fresh-vegetables", "en:fruits", "en:vegetables", "en:fresh-plant-based-foods"}},
}

// AisleFromCategories maps an Open Food Facts product's categories_tags to
// an aisle, or "" when none of them is recognized.
func AisleFromCategories(tags []string) string {
	present := make(map[string]bool, len(tags))
	for _, tag := range tags {
		present[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	for _, rule := range categoryRules {
		for _, tag := range rule.tags {
			if present[tag] {
				return rule.aisle
			}
		}
	}
	return ""
}
