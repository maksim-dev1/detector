package detect

// COCONames is the 80-class label set YOLOv8 is trained on, in model order.
var COCONames = []string{
	"person", "bicycle", "car", "motorcycle", "airplane", "bus", "train", "truck",
	"boat", "traffic light", "fire hydrant", "stop sign", "parking meter", "bench",
	"bird", "cat", "dog", "horse", "sheep", "cow", "elephant", "bear", "zebra",
	"giraffe", "backpack", "umbrella", "handbag", "tie", "suitcase", "frisbee",
	"skis", "snowboard", "sports ball", "kite", "baseball bat", "baseball glove",
	"skateboard", "surfboard", "tennis racket", "bottle", "wine glass", "cup",
	"fork", "knife", "spoon", "bowl", "banana", "apple", "sandwich", "orange",
	"broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair", "couch",
	"potted plant", "bed", "dining table", "toilet", "tv", "laptop", "mouse",
	"remote", "keyboard", "cell phone", "microwave", "oven", "toaster", "sink",
	"refrigerator", "book", "clock", "vase", "scissors", "teddy bear",
	"hair drier", "toothbrush",
}

// ClassIndex returns the model index for a COCO class name, or -1.
func ClassIndex(name string) int {
	for i, n := range COCONames {
		if n == name {
			return i
		}
	}
	return -1
}

// ruNames maps every COCO class to a Russian label. Anything absent falls
// through to the English name.
var ruNames = map[string]string{
	"person": "Человек", "bicycle": "Велосипед", "car": "Машина",
	"motorcycle": "Мотоцикл", "airplane": "Самолёт", "bus": "Автобус",
	"train": "Поезд", "truck": "Грузовик", "boat": "Лодка",
	"traffic light": "Светофор", "fire hydrant": "Гидрант", "stop sign": "Знак «стоп»",
	"parking meter": "Паркомат", "bench": "Скамейка",
	"bird": "Птица", "cat": "Кошка", "dog": "Собака", "horse": "Лошадь",
	"sheep": "Овца", "cow": "Корова", "elephant": "Слон", "bear": "Медведь",
	"zebra": "Зебра", "giraffe": "Жираф",
	"backpack": "Рюкзак", "umbrella": "Зонт", "handbag": "Сумка",
	"tie": "Галстук", "suitcase": "Чемодан",
	"frisbee": "Фрисби", "skis": "Лыжи", "snowboard": "Сноуборд",
	"sports ball": "Мяч", "kite": "Воздушный змей", "baseball bat": "Бейсбольная бита",
	"baseball glove": "Бейсбольная перчатка", "skateboard": "Скейтборд",
	"surfboard": "Доска для сёрфинга", "tennis racket": "Теннисная ракетка",
	"bottle": "Бутылка", "wine glass": "Бокал", "cup": "Чашка", "fork": "Вилка",
	"knife": "Нож", "spoon": "Ложка", "bowl": "Миска",
	"banana": "Банан", "apple": "Яблоко", "sandwich": "Сэндвич", "orange": "Апельсин",
	"broccoli": "Брокколи", "carrot": "Морковь", "hot dog": "Хот-дог",
	"pizza": "Пицца", "donut": "Пончик", "cake": "Торт",
	"chair": "Стул", "couch": "Диван", "potted plant": "Растение в горшке",
	"bed": "Кровать", "dining table": "Обеденный стол", "toilet": "Унитаз",
	"tv": "Телевизор", "laptop": "Ноутбук", "mouse": "Мышь", "remote": "Пульт",
	"keyboard": "Клавиатура", "cell phone": "Телефон",
	"microwave": "Микроволновка", "oven": "Духовка", "toaster": "Тостер",
	"sink": "Раковина", "refrigerator": "Холодильник", "book": "Книга",
	"clock": "Часы", "vase": "Ваза", "scissors": "Ножницы",
	"teddy bear": "Плюшевый мишка", "hair drier": "Фен", "toothbrush": "Зубная щётка",
}

// Groups is the COCO class list split into meaningful sections for display.
var Groups = []struct {
	Title   string
	Classes []string
}{
	{"Люди и транспорт", COCONames[0:9]},
	{"На улице", COCONames[9:14]},
	{"Животные", COCONames[14:24]},
	{"Вещи и сумки", COCONames[24:29]},
	{"Спорт", COCONames[29:39]},
	{"Кухня и посуда", COCONames[39:46]},
	{"Еда", COCONames[46:56]},
	{"Мебель", COCONames[56:62]},
	{"Электроника", COCONames[62:68]},
	{"Техника и быт", COCONames[68:80]},
}

// RussianName returns a human Russian label for a COCO class, or the input.
func RussianName(label string) string {
	if v, ok := ruNames[label]; ok {
		return v
	}
	return label
}
